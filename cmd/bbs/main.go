package main

import (
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"os"
	"time"

	"google.golang.org/grpc"

	"code.cloudfoundry.org/auctioneer"
	"code.cloudfoundry.org/bbs"
	"code.cloudfoundry.org/bbs/cmd/bbs/config"
	"code.cloudfoundry.org/bbs/controllers"
	"code.cloudfoundry.org/bbs/converger"
	"code.cloudfoundry.org/bbs/db"
	etcddb "code.cloudfoundry.org/bbs/db/etcd"
	"code.cloudfoundry.org/bbs/db/migrations"
	"code.cloudfoundry.org/bbs/db/sqldb"
	"code.cloudfoundry.org/bbs/db/sqldb/helpers"
	"code.cloudfoundry.org/bbs/encryption"
	"code.cloudfoundry.org/bbs/encryptor"
	"code.cloudfoundry.org/bbs/events"
	"code.cloudfoundry.org/bbs/format"
	"code.cloudfoundry.org/bbs/guidprovider"
	"code.cloudfoundry.org/bbs/handlers"
	"code.cloudfoundry.org/bbs/metrics"
	"code.cloudfoundry.org/bbs/migration"
	"code.cloudfoundry.org/bbs/models"
	"code.cloudfoundry.org/bbs/taskworkpool"
	"code.cloudfoundry.org/cfhttp"
	"code.cloudfoundry.org/clock"
	"code.cloudfoundry.org/consuladapter"
	"code.cloudfoundry.org/debugserver"
	"code.cloudfoundry.org/lager"
	"code.cloudfoundry.org/lager/lagerflags"
	"code.cloudfoundry.org/locket"
	"code.cloudfoundry.org/locket/lock"
	locketmodels "code.cloudfoundry.org/locket/models"
	"code.cloudfoundry.org/rep"
	"github.com/cloudfoundry/dropsonde"
	etcdclient "github.com/coreos/go-etcd/etcd"
	"github.com/go-sql-driver/mysql"
	"github.com/hashicorp/consul/api"
	"github.com/lib/pq"
	uuid "github.com/nu7hatch/gouuid"
	"github.com/tedsuo/ifrit"
	"github.com/tedsuo/ifrit/grouper"
	"github.com/tedsuo/ifrit/http_server"
	"github.com/tedsuo/ifrit/sigmon"
)

var configFilePath = flag.String(
	"config",
	"",
	"The path to the JSON configuration file.",
)

const (
	dropsondeOrigin           = "bbs"
	bbsWatchRetryWaitDuration = 3 * time.Second
	bbsLockKey                = "bbs"
)

func main() {
	flag.Parse()

	bbsConfig, err := config.NewBBSConfig(*configFilePath)
	if err != nil {
		panic(err.Error())
	}

	cfhttp.Initialize(time.Duration(bbsConfig.CommunicationTimeout))

	logger, reconfigurableSink := lagerflags.NewFromConfig(bbsConfig.SessionName, bbsConfig.LagerConfig)
	logger.Info("starting")

	initializeDropsonde(logger, &bbsConfig)

	clock := clock.NewClock()

	consulClient, err := consuladapter.NewClientFromUrl(bbsConfig.ConsulCluster)
	if err != nil {
		logger.Fatal("new-consul-client-failed", err)
	}

	serviceClient := bbs.NewServiceClient(consulClient, clock)

	_, portString, err := net.SplitHostPort(bbsConfig.ListenAddress)
	if err != nil {
		logger.Fatal("failed-invalid-listen-address", err)
	}
	portNum, err := net.LookupPort("tcp", portString)
	if err != nil {
		logger.Fatal("failed-invalid-listen-port", err)
	}

	_, portString, err = net.SplitHostPort(bbsConfig.HealthAddress)
	if err != nil {
		logger.Fatal("failed-invalid-health-address", err)
	}
	_, err = net.LookupPort("tcp", portString)
	if err != nil {
		logger.Fatal("failed-invalid-health-port", err)
	}

	registrationRunner := initializeRegistrationRunner(logger, consulClient, portNum, clock)

	var activeDB db.DB
	var sqlDB *sqldb.SQLDB
	var sqlConn *sql.DB
	var storeClient etcddb.StoreClient
	var etcdDB *etcddb.ETCDDB

	key, keys, err := bbsConfig.EncryptionConfig.Parse()
	if err != nil {
		logger.Fatal("cannot-setup-encryption", err)
	}
	keyManager, err := encryption.NewKeyManager(key, keys)
	if err != nil {
		logger.Fatal("cannot-setup-encryption", err)
	}
	cryptor := encryption.NewCryptor(keyManager, rand.Reader)

	etcdOptions, err := bbsConfig.ETCDConfig.Validate()
	if err != nil {
		logger.Fatal("etcd-validation-failed", err)
	}

	if etcdOptions.IsConfigured {
		storeClient = initializeEtcdStoreClient(logger, etcdOptions)
		etcdDB = initializeEtcdDB(logger, cryptor, storeClient, serviceClient, &bbsConfig)
		activeDB = etcdDB
	}

	// If SQL database info is passed in, use SQL instead of ETCD
	if bbsConfig.DatabaseDriver != "" && bbsConfig.DatabaseConnectionString != "" {
		var err error
		connectionString := appendExtraConnectionStringParam(logger,
			bbsConfig.DatabaseDriver,
			bbsConfig.DatabaseConnectionString,
			bbsConfig.SQLCACertFile,
		)

		sqlConn, err = sql.Open(bbsConfig.DatabaseDriver, connectionString)
		if err != nil {
			logger.Fatal("failed-to-open-sql", err)
		}
		defer sqlConn.Close()
		sqlConn.SetMaxOpenConns(bbsConfig.MaxOpenDatabaseConnections)
		sqlConn.SetMaxIdleConns(bbsConfig.MaxIdleDatabaseConnections)

		err = sqlConn.Ping()
		if err != nil {
			logger.Fatal("sql-failed-to-connect", err)
		}

		sqlDB = sqldb.NewSQLDB(sqlConn,
			bbsConfig.ConvergenceWorkers,
			bbsConfig.UpdateWorkers,
			format.ENCRYPTED_PROTO,
			cryptor,
			guidprovider.DefaultGuidProvider,
			clock,
			bbsConfig.DatabaseDriver,
		)
		err = sqlDB.SetIsolationLevel(logger, helpers.IsolationLevelReadCommitted)
		if err != nil {
			logger.Fatal("sql-failed-to-set-isolation-level", err)
		}

		err = sqlDB.CreateConfigurationsTable(logger)
		if err != nil {
			logger.Fatal("sql-failed-create-configurations-table", err)
		}
		activeDB = sqlDB
	}

	if activeDB == nil {
		logger.Fatal("no-database-configured", errors.New("no database configured"))
	}

	encryptor := encryptor.New(logger, activeDB, keyManager, cryptor, clock)

	migrationsDone := make(chan struct{})

	migrationManager := migration.NewManager(
		logger,
		etcdDB,
		storeClient,
		sqlDB,
		sqlConn,
		cryptor,
		migrations.Migrations,
		migrationsDone,
		clock,
		bbsConfig.DatabaseDriver,
	)

	desiredHub := events.NewHub()
	actualHub := events.NewHub()

	repTLSConfig := &rep.TLSConfig{
		RequireTLS:      bbsConfig.RepRequireTLS,
		CaCertFile:      bbsConfig.RepCACert,
		CertFile:        bbsConfig.RepClientCert,
		KeyFile:         bbsConfig.RepClientKey,
		ClientCacheSize: bbsConfig.RepClientSessionCacheSize,
	}

	httpClient := cfhttp.NewClient()
	repClientFactory, err := rep.NewClientFactory(httpClient, httpClient, repTLSConfig)
	if err != nil {
		logger.Fatal("new-rep-client-factory-failed", err)
	}

	auctioneerClient := initializeAuctioneerClient(logger, &bbsConfig)

	exitChan := make(chan struct{})

	var accessLogger lager.Logger
	if bbsConfig.AccessLogPath != "" {
		accessLogger = lager.NewLogger("bbs-access")
		file, err := os.OpenFile(bbsConfig.AccessLogPath, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
		if err != nil {
			logger.Error("invalid-access-log-path", err, lager.Data{"access-log-path": bbsConfig.AccessLogPath})
			os.Exit(1)
		}
		accessLogger.RegisterSink(lager.NewWriterSink(file, lager.INFO))
	}

	var tlsConfig *tls.Config
	if bbsConfig.RequireSSL {
		tlsConfig, err = cfhttp.NewTLSConfig(bbsConfig.CertFile, bbsConfig.KeyFile, bbsConfig.CaFile)
		if err != nil {
			logger.Fatal("tls-configuration-failed", err)
		}
	}

	cbWorkPool := taskworkpool.New(logger, bbsConfig.TaskCallbackWorkers, taskworkpool.HandleCompletedTask, tlsConfig)

	handler := handlers.New(
		logger,
		accessLogger,
		bbsConfig.UpdateWorkers,
		bbsConfig.ConvergenceWorkers,
		activeDB,
		desiredHub,
		actualHub,
		cbWorkPool,
		serviceClient,
		auctioneerClient,
		repClientFactory,
		migrationsDone,
		exitChan,
	)

	metricsNotifier := metrics.NewPeriodicMetronNotifier(logger)

	actualLRPController := controllers.NewActualLRPLifecycleController(activeDB, activeDB, activeDB, auctioneerClient, serviceClient, repClientFactory, actualHub)
	lrpConvergenceController := controllers.NewLRPConvergenceController(logger,
		activeDB,
		actualHub,
		auctioneerClient,
		serviceClient,
		actualLRPController,
		bbsConfig.ConvergenceWorkers,
	)
	taskController := controllers.NewTaskController(activeDB, cbWorkPool, auctioneerClient, serviceClient, repClientFactory)

	convergerProcess := converger.New(
		logger,
		clock,
		lrpConvergenceController,
		taskController,
		serviceClient,
		time.Duration(bbsConfig.ConvergeRepeatInterval),
		time.Duration(bbsConfig.KickTaskDuration),
		time.Duration(bbsConfig.ExpirePendingTaskDuration),
		time.Duration(bbsConfig.ExpireCompletedTaskDuration),
	)

	var server ifrit.Runner
	if tlsConfig != nil {
		server = http_server.NewTLSServer(bbsConfig.ListenAddress, handler, tlsConfig)
	} else {
		server = http_server.New(bbsConfig.ListenAddress, handler)
	}

	healthcheckServer := http_server.New(bbsConfig.HealthAddress, http.HandlerFunc(healthCheckHandler))

	members := grouper.Members{
		{"healthcheck", healthcheckServer},
		{"workpool", cbWorkPool},
		{"server", server},
		{"migration-manager", migrationManager},
		{"encryptor", encryptor},
		{"hub-maintainer", hubMaintainer(logger, desiredHub, actualHub)},
		{"metrics", *metricsNotifier},
		{"converger", convergerProcess},
		{"registration-runner", registrationRunner},
	}

	if bbsConfig.DebugAddress != "" {
		members = append(grouper.Members{
			{"debug-server", debugserver.Runner(bbsConfig.DebugAddress, reconfigurableSink)},
		}, members...)
	}

	locks := []grouper.Member{}

	if !bbsConfig.SkipConsulLock {
		maintainer := initializeLockMaintainer(logger, serviceClient, &bbsConfig)
		locks = append(locks, grouper.Member{"lock-maintainer", maintainer})
	}

	if bbsConfig.LocketAddress != "" {
		conn, err := grpc.Dial(bbsConfig.LocketAddress, grpc.WithInsecure())
		if err != nil {
			logger.Fatal("failed-to-connect-to-locket", err)
		}
		locketClient := locketmodels.NewLocketClient(conn)

		guid, err := uuid.NewV4()
		if err != nil {
			logger.Fatal("failed-to-generate-guid", err)
		}

		lockIdentifier := &locketmodels.Resource{
			Key:   bbsLockKey,
			Owner: guid.String(),
		}

		locks = append(locks, grouper.Member{"sql-lock", lock.NewLockRunner(
			logger,
			locketClient,
			lockIdentifier,
			locket.DefaultSessionTTLInSeconds,
			clock,
			locket.RetryInterval,
		)})
	}

	if len(locks) < 1 {
		logger.Fatal("no-locks-configured", errors.New("Lock configuration must be provided"))
	}

	members = insertToMembersAfter(
		members,
		"healthcheck",
		locks...,
	)

	group := grouper.NewOrdered(os.Interrupt, members)

	monitor := ifrit.Invoke(sigmon.New(group))
	go func() {
		// If a handler writes to this channel, we've hit an unrecoverable error
		// and should shut down (cleanly)
		<-exitChan
		monitor.Signal(os.Interrupt)
	}()

	logger.Info("started")

	err = <-monitor.Wait()
	if sqlConn != nil {
		sqlConn.Close()
	}
	if err != nil {
		logger.Error("exited-with-failure", err)
		os.Exit(1)
	}

	logger.Info("exited")
}

func appendExtraConnectionStringParam(logger lager.Logger, driverName, databaseConnectionString, sqlCACertFile string) string {
	switch driverName {
	case "mysql":
		cfg, err := mysql.ParseDSN(databaseConnectionString)
		if err != nil {
			logger.Fatal("invalid-db-connection-string", err, lager.Data{"connection-string": databaseConnectionString})
		}

		if sqlCACertFile != "" {
			certBytes, err := ioutil.ReadFile(sqlCACertFile)
			if err != nil {
				logger.Fatal("failed-to-read-sql-ca-file", err)
			}

			caCertPool := x509.NewCertPool()
			if ok := caCertPool.AppendCertsFromPEM(certBytes); !ok {
				logger.Fatal("failed-to-parse-sql-ca", err)
			}

			tlsConfig := &tls.Config{
				InsecureSkipVerify: false,
				RootCAs:            caCertPool,
			}

			mysql.RegisterTLSConfig("bbs-tls", tlsConfig)
			cfg.TLSConfig = "bbs-tls"
		}
		cfg.Timeout = 10 * time.Minute
		cfg.ReadTimeout = 10 * time.Minute
		cfg.WriteTimeout = 10 * time.Minute
		databaseConnectionString = cfg.FormatDSN()
	case "postgres":
		var err error
		databaseConnectionString, err = pq.ParseURL(databaseConnectionString)
		if err != nil {
			logger.Fatal("invalid-db-connection-string", err, lager.Data{"connection-string": databaseConnectionString})
		}
		if sqlCACertFile == "" {
			databaseConnectionString = databaseConnectionString + " sslmode=disable"
		} else {
			databaseConnectionString = fmt.Sprintf("%s sslmode=verify-ca sslrootcert=%s", databaseConnectionString, sqlCACertFile)
		}
	case "mssql":
		if sqlCACertFile != "" {
			databaseConnectionString = fmt.Sprintf("%s;encrypt=true;certificate=%s", databaseConnectionString, sqlCACertFile)
		}
	}

	return databaseConnectionString
}

func healthCheckHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func hubMaintainer(logger lager.Logger, desiredHub, actualHub events.Hub) ifrit.RunFunc {
	return func(signals <-chan os.Signal, ready chan<- struct{}) error {
		logger := logger.Session("hub-maintainer")
		close(ready)
		logger.Info("started")
		defer logger.Info("finished")

		<-signals
		err := desiredHub.Close()
		if err != nil {
			logger.Error("error-closing-desired-hub", err)
		}
		err = actualHub.Close()
		if err != nil {
			logger.Error("error-closing-actual-hub", err)
		}
		return nil
	}
}

func initializeRegistrationRunner(
	logger lager.Logger,
	consulClient consuladapter.Client,
	port int,
	clock clock.Clock) ifrit.Runner {
	registration := &api.AgentServiceRegistration{
		Name: "bbs",
		Port: port,
		Check: &api.AgentServiceCheck{
			TTL: "20s",
		},
	}
	return locket.NewRegistrationRunner(logger, registration, consulClient, locket.RetryInterval, clock)
}

func initializeLockMaintainer(logger lager.Logger, serviceClient bbs.ServiceClient, bbsConfig *config.BBSConfig) ifrit.Runner {
	uuid, err := uuid.NewV4()
	if err != nil {
		logger.Fatal("Couldn't generate uuid", err)
	}

	if bbsConfig.AdvertiseURL == "" {
		logger.Fatal("Advertise URL must be specified", nil)
	}

	bbsPresence := models.NewBBSPresence(uuid.String(), bbsConfig.AdvertiseURL)
	lockMaintainer, err := serviceClient.NewBBSLockRunner(logger,
		&bbsPresence,
		time.Duration(bbsConfig.LockRetryInterval),
		time.Duration(bbsConfig.LockTTL),
	)
	if err != nil {
		logger.Fatal("Couldn't create lock maintainer", err)
	}

	return lockMaintainer
}

func initializeAuctioneerClient(logger lager.Logger, bbsConfig *config.BBSConfig) auctioneer.Client {
	if bbsConfig.AuctioneerAddress == "" {
		logger.Fatal("auctioneer-address-validation-failed", errors.New("auctioneerAddress is required"))
	}

	if bbsConfig.AuctioneerCACert != "" || bbsConfig.AuctioneerClientCert != "" || bbsConfig.AuctioneerClientKey != "" {
		client, err := auctioneer.NewSecureClient(bbsConfig.AuctioneerAddress,
			bbsConfig.AuctioneerCACert,
			bbsConfig.AuctioneerClientCert,
			bbsConfig.AuctioneerClientKey,
			bbsConfig.AuctioneerRequireTLS,
		)
		if err != nil {
			logger.Fatal("failed-to-construct-auctioneer-client", err)
		}
		return client
	}

	return auctioneer.NewClient(bbsConfig.AuctioneerAddress)
}

func initializeDropsonde(logger lager.Logger, bbsConfig *config.BBSConfig) {
	dropsondeDestination := fmt.Sprint("localhost:", bbsConfig.DropsondePort)
	err := dropsonde.Initialize(dropsondeDestination, dropsondeOrigin)
	if err != nil {
		logger.Error("failed-to-initialize-dropsonde", err)
	}
}

func initializeEtcdDB(
	logger lager.Logger,
	cryptor encryption.Cryptor,
	storeClient etcddb.StoreClient,
	serviceClient bbs.ServiceClient,
	bbsConfig *config.BBSConfig,
) *etcddb.ETCDDB {
	return etcddb.NewETCD(
		format.ENCRYPTED_PROTO,
		bbsConfig.ConvergenceWorkers,
		bbsConfig.UpdateWorkers,
		time.Duration(bbsConfig.DesiredLRPCreationTimeout),
		cryptor,
		storeClient,
		clock.NewClock(),
	)
}

func initializeEtcdStoreClient(logger lager.Logger, etcdOptions *etcddb.ETCDOptions) etcddb.StoreClient {
	var etcdClient *etcdclient.Client
	var tr *http.Transport

	if etcdOptions.IsSSL {
		if etcdOptions.CertFile == "" || etcdOptions.KeyFile == "" {
			logger.Fatal("failed-to-construct-etcd-tls-client", errors.New("Require both cert and key path"))
		}

		var err error
		etcdClient, err = etcdclient.NewTLSClient(etcdOptions.ClusterUrls, etcdOptions.CertFile, etcdOptions.KeyFile, etcdOptions.CAFile)
		if err != nil {
			logger.Fatal("failed-to-construct-etcd-tls-client", err)
		}

		tlsCert, err := tls.LoadX509KeyPair(etcdOptions.CertFile, etcdOptions.KeyFile)
		if err != nil {
			logger.Fatal("failed-to-construct-etcd-tls-client", err)
		}

		tlsConfig := &tls.Config{
			Certificates:       []tls.Certificate{tlsCert},
			InsecureSkipVerify: true,
			ClientSessionCache: tls.NewLRUClientSessionCache(etcdOptions.ClientSessionCacheSize),
		}
		tr = &http.Transport{
			TLSClientConfig:     tlsConfig,
			Dial:                etcdClient.DefaultDial,
			MaxIdleConnsPerHost: etcdOptions.MaxIdleConnsPerHost,
		}
		etcdClient.SetTransport(tr)
		etcdClient.AddRootCA(etcdOptions.CAFile)
	} else {
		etcdClient = etcdclient.NewClient(etcdOptions.ClusterUrls)
	}
	etcdClient.SetConsistency(etcdclient.STRONG_CONSISTENCY)

	return etcddb.NewStoreClient(etcdClient)
}

func insertToMembersAfter(members grouper.Members, name string, extraMembers ...grouper.Member) grouper.Members {
	for i, m := range members {
		if m.Name == name {
			beforeMembers := members[:i+1]
			afterMembers := members[i+1:]
			return append(beforeMembers, append(extraMembers, afterMembers...)...)
		}
	}
	panic("member-does-not-exist")
}
