package sqlrunner

import (
	"database/sql"
	"fmt"
	"os"

	_ "github.com/denisenkom/go-mssqldb"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

// MsSQLRunner is responsible for creating and tearing down a test database in
// a Microsoft SQL instance. This runner assumes mssql is already running
// on Azure.
// To run the test, you need to specific MSSQL_CONNECTION_STRING in env.
// example: SQL_FLAVOR="mssql" MSSQL_CONNECTION_STRING="server=<server>.database.windows.net;user id=<username>;password=<password>;database=diego;port=1433"
type MsSQLRunner struct {
	sqlDBName string
	db        *sql.DB
}

func NewMsSQLRunner(sqlDBName string) *MsSQLRunner {
	return &MsSQLRunner{
		sqlDBName: sqlDBName,
	}
}

func (m *MsSQLRunner) Run(signals <-chan os.Signal, ready chan<- struct{}) error {
	defer GinkgoRecover()

	db_connection_string := os.Getenv("MSSQL_CONNECTION_STRING")
	if db_connection_string == "" {
		panic(fmt.Sprintf("You must specify MSSQL_CONNECTION_STRING when running test for mssql"))
	}

	var err error
	m.db, err = sql.Open("mssql", db_connection_string)
	Expect(err).NotTo(HaveOccurred())
	Expect(m.db.Ping()).NotTo(HaveOccurred())

	//m.db.Exec(fmt.Sprintf("DROP DATABASE %s", m.sqlDBName))
	//_, err = m.db.Exec(fmt.Sprintf("CREATE DATABASE %s", m.sqlDBName))
	//Expect(err).NotTo(HaveOccurred())

	//m.db, err = sql.Open("mssql", fmt.Sprintf("diego:diego_password@/%s", m.sqlDBName))
	//Expect(err).NotTo(HaveOccurred())
	//Expect(m.db.Ping()).NotTo(HaveOccurred())

	close(ready)

	<-signals

	_, err = m.db.Exec(fmt.Sprintf("DROP DATABASE %s", m.sqlDBName))
	Expect(err).NotTo(HaveOccurred())
	Expect(m.db.Close()).To(Succeed())
	m.db = nil

	return nil
}

func (m *MsSQLRunner) ConnectionString() string {
	return os.Getenv("MSSQL_CONNECTION_STRING")
}

func (m *MsSQLRunner) DriverName() string {
	return "mssql"
}

func (m *MsSQLRunner) DB() *sql.DB {
	return m.db
}

func (m *MsSQLRunner) Reset() {
	var truncateTablesSQL = []string{
		"TRUNCATE TABLE domains",
		"TRUNCATE TABLE configurations",
		"TRUNCATE TABLE tasks",
		"TRUNCATE TABLE desired_lrps",
		"TRUNCATE TABLE actual_lrps",
	}
	for _, query := range truncateTablesSQL {
		result, err := m.db.Exec(query)
		//switch err := err.(type) {
		//TODO
		//case *mysql.MySQLError:
		//	if err.Number == 1146 {
				// missing table error, it's fine because we're trying to truncate it
		//		continue
		//	}
		//}

		Expect(err).NotTo(HaveOccurred())
		Expect(result.RowsAffected()).To(BeEquivalentTo(0))
	}
}
