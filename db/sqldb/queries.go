package sqldb

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"code.cloudfoundry.org/bbs/models"
	"code.cloudfoundry.org/lager"
)

const (
	MySQL    = "mysql"
	Postgres = "postgres"
	MSSQL    = "mssql"
)

const (
	IsolationLevelReadUncommitted = "READ UNCOMMITTED"
	IsolationLevelReadCommitted   = "READ COMMITTED"
	IsolationLevelSerializable    = "SERIALIZABLE"
	IsolationLevelRepeatableRead  = "REPEATABLE READ"
)

type RowLock bool

const (
	LockRow   RowLock = true
	NoLockRow RowLock = false
)

type SQLAttributes map[string]interface{}

type ColumnList []string

const (
	tasksTable       = "tasks"
	desiredLRPsTable = "desired_lrps"
	actualLRPsTable  = "actual_lrps"
	domainsTable     = "domains"
)

var (
	schedulingInfoColumns = ColumnList{
		desiredLRPsTable + ".process_guid",
		desiredLRPsTable + ".domain",
		desiredLRPsTable + ".log_guid",
		desiredLRPsTable + ".annotation",
		desiredLRPsTable + ".instances",
		desiredLRPsTable + ".memory_mb",
		desiredLRPsTable + ".disk_mb",
		desiredLRPsTable + ".max_pids",
		desiredLRPsTable + ".rootfs",
		desiredLRPsTable + ".routes",
		desiredLRPsTable + ".volume_placement",
		desiredLRPsTable + ".modification_tag_epoch",
		desiredLRPsTable + ".modification_tag_index",
		desiredLRPsTable + ".placement_tags",
	}

	desiredLRPColumns = append(schedulingInfoColumns,
		desiredLRPsTable+".run_info",
	)

	taskColumns = ColumnList{
		tasksTable + ".guid",
		tasksTable + ".domain",
		tasksTable + ".updated_at",
		tasksTable + ".created_at",
		tasksTable + ".first_completed_at",
		tasksTable + ".state",
		tasksTable + ".cell_id",
		tasksTable + ".result",
		tasksTable + ".failed",
		tasksTable + ".failure_reason",
		tasksTable + ".task_definition",
	}

	actualLRPColumns = ColumnList{
		actualLRPsTable + ".process_guid",
		actualLRPsTable + ".instance_index",
		actualLRPsTable + ".evacuating",
		actualLRPsTable + ".domain",
		actualLRPsTable + ".state",
		actualLRPsTable + ".instance_guid",
		actualLRPsTable + ".cell_id",
		actualLRPsTable + ".placement_error",
		actualLRPsTable + ".since",
		actualLRPsTable + ".net_info",
		actualLRPsTable + ".modification_tag_epoch",
		actualLRPsTable + ".modification_tag_index",
		actualLRPsTable + ".crash_count",
		actualLRPsTable + ".crash_reason",
	}

	domainColumns = ColumnList{
		domainsTable + ".domain",
	}
)

func (db *SQLDB) CreateConfigurationsTable(logger lager.Logger) error {
	switch db.flavor {
	case MSSQL:
		_, err := db.db.Exec(`
			IF NOT EXISTS (SELECT * FROM sysobjects WHERE name='configurations' AND xtype='U')
			CREATE TABLE configurations(
			id VARCHAR(255) PRIMARY KEY,
			value VARCHAR(255)
			)
		`)
		if err != nil {
			return err
		}
	default:
		_, err := db.db.Exec(`
			CREATE TABLE IF NOT EXISTS configurations(
			id VARCHAR(255) PRIMARY KEY,
			value VARCHAR(255)
			)
		`)
		if err != nil {
			return err
		}
	}

	return nil
}

func (db *SQLDB) SetIsolationLevel(logger lager.Logger, level string) error {
	logger = logger.Session("set-isolation-level", lager.Data{"level": level})
	logger.Info("starting")
	defer logger.Info("done")

	var query string
	if db.flavor == MySQL {
		query = fmt.Sprintf("SET SESSION TRANSACTION ISOLATION LEVEL %s", level)
	} else if db.flavor == Postgres {
		query = fmt.Sprintf("SET SESSION CHARACTERISTICS AS TRANSACTION ISOLATION LEVEL %s", level)
	} else if db.flavor == MSSQL {
		query = fmt.Sprintf("SET TRANSACTION ISOLATION LEVEL %s", level)
	} else {
		panic("database flavor not implemented: " + db.flavor)
	}

	_, err := db.db.Exec(query)
	return err
}

// Takes in a query that uses question marks to represent unbound SQL parameters
// and converts those to '$1, $2', etc. if the DB flavor is postgres.
// Takes in a query that uses MEDIUMTEXT to create table columns and converts
// those to TEXT if the DB flavor is postgres
// e.g., `SELECT * FROM table_name WHERE col = ? AND col2 = ?` becomes
//       `SELECT * FROM table_name WHERE col = $1 AND col2 = $2`
// e.g., `CREATE TABLE desired_lrps(
//	 annotation MEDIUMTEXT
// )` becomes
// `CREATE TABLE desired_lrps(
//	 annotation TEXT
// )`
func RebindForFlavor(query, flavor string) string {
	switch flavor {
	case MySQL:
		return query
	case Postgres:
		strParts := strings.Split(query, "?")
		for i := 1; i < len(strParts); i++ {
			strParts[i-1] = fmt.Sprintf("%s$%d", strParts[i-1], i)
		}
		return strings.Replace(strings.Join(strParts, ""), "MEDIUMTEXT", "TEXT", -1)
	case MSSQL:
		query = strings.Replace(query, "MEDIUMTEXT", "NVARCHAR(MAX)", -1)
		query = strings.Replace(query, "TEXT", "NVARCHAR(MAX)", -1)
		return query
	default:
		panic("database flavor not implemented: " + flavor)
	}
}

func (db *SQLDB) selectLRPInstanceCounts(logger lager.Logger, q Queryable) (*sql.Rows, error) {
	var query string
	columns := schedulingInfoColumns

	switch db.flavor {
	case Postgres:
		columns = append(columns, "COUNT(actual_lrps.instance_index) AS actual_instances")
		columns = append(columns, "STRING_AGG(actual_lrps.instance_index::text, ',') AS existing_indices")
		query = fmt.Sprintf(`
			SELECT %s
				FROM desired_lrps
				LEFT OUTER JOIN actual_lrps ON desired_lrps.process_guid = actual_lrps.process_guid AND actual_lrps.evacuating = false
				GROUP BY desired_lrps.process_guid
				HAVING COUNT(actual_lrps.instance_index) <> desired_lrps.instances
			`,
			strings.Join(columns, ", "),
		)

	case MySQL:
		columns = append(columns, "COUNT(actual_lrps.instance_index) AS actual_instances")
		columns = append(columns, "GROUP_CONCAT(actual_lrps.instance_index) AS existing_indices")
		query = fmt.Sprintf(`
			SELECT %s
				FROM desired_lrps
				LEFT OUTER JOIN actual_lrps ON desired_lrps.process_guid = actual_lrps.process_guid AND actual_lrps.evacuating = false
				GROUP BY desired_lrps.process_guid
				HAVING COUNT(actual_lrps.instance_index) <> desired_lrps.instances
			`,
			strings.Join(columns, ", "),
		)
	case MSSQL:
		query = fmt.Sprintf(`
			SELECT %s,
				T1.actual_instances,
				T1.existing_indices
			FROM
				(SELECT desired_lrps.process_guid,
					desired_lrps.instances,
					COUNT(actual_lrps.instance_index) AS actual_instances,
					STUFF ((SELECT ',' + CAST(al.instance_index AS VARCHAR)
							FROM actual_lrps al
							WHERE al.process_guid = desired_lrps.process_guid
							FOR XML PATH('')
							), 1, 1, '') AS existing_indices
				FROM desired_lrps
				LEFT OUTER JOIN actual_lrps ON desired_lrps.process_guid = actual_lrps.process_guid AND actual_lrps.evacuating = 0
				GROUP BY desired_lrps.process_guid, desired_lrps.instances
				HAVING COUNT(actual_lrps.instance_index) <> desired_lrps.instances
				) T1
			LEFT JOIN desired_lrps T2 ON T2.process_guid = T1.process_guid AND T2.instances = T1.instances
			`,
			strings.Replace(strings.Join(columns, ", "), desiredLRPsTable, "T2", -1),
		)
	default:
		// totally shouldn't happen
		panic("database flavor not implemented: " + db.flavor)
	}

	return q.Query(query)
}
func (db *SQLDB) selectOrphanedActualLRPs(logger lager.Logger, q Queryable) (*sql.Rows, error) {
	query := `
    SELECT actual_lrps.process_guid, actual_lrps.instance_index, actual_lrps.domain
      FROM actual_lrps
      JOIN domains ON actual_lrps.domain = domains.domain
      LEFT JOIN desired_lrps ON actual_lrps.process_guid = desired_lrps.process_guid
      WHERE actual_lrps.evacuating = ? AND desired_lrps.process_guid IS NULL
		`

	return q.Query(db.rebind(query), false)
}

func (db *SQLDB) selectLRPsWithMissingCells(logger lager.Logger, q Queryable, cellSet models.CellSet) (*sql.Rows, error) {
	bindings := make([]interface{}, 0, len(cellSet))
	wheres := []string{"actual_lrps.evacuating = ?"}
	bindings = append(bindings, false)

	if len(cellSet) > 0 {
		wheres = append(wheres, fmt.Sprintf("actual_lrps.cell_id NOT IN (%s)", questionMarks(len(cellSet))))
		wheres = append(wheres, "actual_lrps.cell_id <> ''")
		for cellID := range cellSet {
			bindings = append(bindings, cellID)
		}
	}

	query := fmt.Sprintf(`
		SELECT %s
			FROM desired_lrps
			JOIN actual_lrps ON desired_lrps.process_guid = actual_lrps.process_guid
			WHERE %s
		`,
		strings.Join(append(schedulingInfoColumns, "actual_lrps.instance_index"), ", "),
		strings.Join(wheres, " AND "),
	)

	return q.Query(db.rebind(query), bindings...)
}

func (db *SQLDB) selectCrashedLRPs(logger lager.Logger, q Queryable) (*sql.Rows, error) {
	query := fmt.Sprintf(`
		SELECT %s
			FROM desired_lrps
			JOIN actual_lrps ON desired_lrps.process_guid = actual_lrps.process_guid
			WHERE actual_lrps.state = ? AND actual_lrps.evacuating = ?
		`,
		strings.Join(
			append(schedulingInfoColumns, "actual_lrps.instance_index", "actual_lrps.since", "actual_lrps.crash_count"),
			", ",
		),
	)

	return q.Query(db.rebind(query), models.ActualLRPStateCrashed, false)
}

func (db *SQLDB) selectStaleUnclaimedLRPs(logger lager.Logger, q Queryable, now time.Time) (*sql.Rows, error) {
	query := fmt.Sprintf(`
		SELECT %s
			FROM desired_lrps
			JOIN actual_lrps ON desired_lrps.process_guid = actual_lrps.process_guid
			WHERE actual_lrps.state = ? AND actual_lrps.since < ? AND actual_lrps.evacuating = ?
		`,
		strings.Join(append(schedulingInfoColumns, "actual_lrps.instance_index"), ", "),
	)

	return q.Query(db.rebind(query),
		models.ActualLRPStateUnclaimed,
		now.Add(-models.StaleUnclaimedActualLRPDuration).UnixNano(),
		false,
	)
}

func (db *SQLDB) countDesiredInstances(logger lager.Logger, q Queryable) int {
	query := `
		SELECT COALESCE(SUM(desired_lrps.instances), 0) AS desired_instances
			FROM desired_lrps
	`

	var desiredInstances int
	row := q.QueryRow(db.rebind(query))
	err := row.Scan(&desiredInstances)
	if err != nil {
		logger.Error("failed-desired-instances-query", err)
	}
	return desiredInstances
}

func (db *SQLDB) countActualLRPsByState(logger lager.Logger, q Queryable) (claimedCount, unclaimedCount, runningCount, crashedCount, crashingDesiredCount int) {
	var query string
	switch db.flavor {
	case Postgres:
		query = `
			SELECT
				COUNT(*) FILTER (WHERE actual_lrps.state = $1) AS claimed_instances,
				COUNT(*) FILTER (WHERE actual_lrps.state = $2) AS unclaimed_instances,
				COUNT(*) FILTER (WHERE actual_lrps.state = $3) AS running_instances,
				COUNT(*) FILTER (WHERE actual_lrps.state = $4) AS crashed_instances,
				COUNT(DISTINCT process_guid) FILTER (WHERE actual_lrps.state = $5) AS crashing_desireds
			FROM actual_lrps
			WHERE evacuating = $6
		`
	case MySQL:
		query = `
			SELECT
				COUNT(IF(actual_lrps.state = ?, 1, NULL)) AS claimed_instances,
				COUNT(IF(actual_lrps.state = ?, 1, NULL)) AS unclaimed_instances,
				COUNT(IF(actual_lrps.state = ?, 1, NULL)) AS running_instances,
				COUNT(IF(actual_lrps.state = ?, 1, NULL)) AS crashed_instances,
				COUNT(DISTINCT IF(state = ?, process_guid, NULL)) AS crashing_desireds
			FROM actual_lrps
			WHERE evacuating = ?
		`
	case MSSQL:
		query = `
			SELECT
				COUNT(CASE WHEN actual_lrps.state = ? THEN 1 ELSE NULL END) AS claimed_instances,
				COUNT(CASE WHEN actual_lrps.state = ? THEN 1 ELSE NULL END) AS unclaimed_instances,
				COUNT(CASE WHEN actual_lrps.state = ? THEN 1 ELSE NULL END) AS running_instances,
				COUNT(CASE WHEN actual_lrps.state = ? THEN 1 ELSE NULL END) AS crashed_instances,
				COUNT(DISTINCT CASE WHEN state = ? THEN process_guid ELSE NULL END) AS crashing_desireds
			FROM actual_lrps
			WHERE evacuating = ?
		`
	default:
		// totally shouldn't happen
		panic("database flavor not implemented: " + db.flavor)
	}

	row := db.db.QueryRow(query, models.ActualLRPStateClaimed, models.ActualLRPStateUnclaimed, models.ActualLRPStateRunning, models.ActualLRPStateCrashed, models.ActualLRPStateCrashed, false)
	err := row.Scan(&claimedCount, &unclaimedCount, &runningCount, &crashedCount, &crashingDesiredCount)
	if err != nil {
		logger.Error("failed-counting-actual-lrps", err)
	}
	return
}

func (db *SQLDB) countTasksByState(logger lager.Logger, q Queryable) (pendingCount, runningCount, completedCount, resolvingCount int) {
	var query string
	switch db.flavor {
	case Postgres:
		query = `
			SELECT
				COUNT(*) FILTER (WHERE state = $1) AS pending_tasks,
				COUNT(*) FILTER (WHERE state = $2) AS running_tasks,
				COUNT(*) FILTER (WHERE state = $3) AS completed_tasks,
				COUNT(*) FILTER (WHERE state = $4) AS resolving_tasks
			FROM tasks
		`
	case MySQL:
		query = `
			SELECT
				COUNT(IF(state = ?, 1, NULL)) AS pending_tasks,
				COUNT(IF(state = ?, 1, NULL)) AS running_tasks,
				COUNT(IF(state = ?, 1, NULL)) AS completed_tasks,
				COUNT(IF(state = ?, 1, NULL)) AS resolving_tasks
			FROM tasks
		`
	case MSSQL:
		query = `
			SELECT
				COUNT(CASE WHEN state = ? THEN 1 ELSE NULL END) AS pending_tasks,
				COUNT(CASE WHEN state = ? THEN 1 ELSE NULL END) AS running_tasks,
				COUNT(CASE WHEN state = ? THEN 1 ELSE NULL END) AS completed_tasks,
				COUNT(CASE WHEN state = ? THEN 1 ELSE NULL END) AS resolving_tasks
			FROM tasks
		`
	default:
		// totally shouldn't happen
		panic("database flavor not implemented: " + db.flavor)
	}

	row := db.db.QueryRow(query, models.Task_Pending, models.Task_Running, models.Task_Completed, models.Task_Resolving)
	err := row.Scan(&pendingCount, &runningCount, &completedCount, &resolvingCount)
	if err != nil {
		logger.Error("failed-counting-tasks", err)
	}
	return
}

// SELECT <columns> FROM <table> WHERE ... LIMIT 1 [FOR UPDATE]
func (db *SQLDB) one(logger lager.Logger, q Queryable, table string,
	columns ColumnList, lockRow RowLock,
	wheres string, whereBindings ...interface{},
) *sql.Row {
	var query string

	if db.flavor == MSSQL {
		lockClause := ""
		if lockRow {
			lockClause = " WITH (UPDLOCK)"
		}
		query = fmt.Sprintf("SELECT TOP 1 %s FROM %s%s\n", strings.Join(columns, ", "), table, lockClause)

		if len(wheres) > 0 {
			query += "WHERE " + wheres
		}
	} else {
		query = fmt.Sprintf("SELECT %s FROM %s\n", strings.Join(columns, ", "), table)

		if len(wheres) > 0 {
			query += "WHERE " + wheres
		}

		query += "\nLIMIT 1"

		if lockRow {
			query += "\nFOR UPDATE"
		}
	}

	return q.QueryRow(db.rebind(query), whereBindings...)
}

// SELECT <columns> FROM <table> WHERE ... [FOR UPDATE]
func (db *SQLDB) all(logger lager.Logger, q Queryable, table string,
	columns ColumnList, lockRow RowLock,
	wheres string, whereBindings ...interface{},
) (*sql.Rows, error) {
	var query string

	if db.flavor == MSSQL {
		lockClause := ""

		if lockRow {
			lockClause = " WITH (UPDLOCK)"
		}

		query = fmt.Sprintf("SELECT %s FROM %s%s\n", strings.Join(columns, ", "), table, lockClause)

		if len(wheres) > 0 {
			 query += "WHERE " + wheres
		}
	} else {

		query = fmt.Sprintf("SELECT %s FROM %s\n", strings.Join(columns, ", "), table)

		if len(wheres) > 0 {
			query += "WHERE " + wheres
		}

		if lockRow {
			query += "\nFOR UPDATE"
		}
	}

	return q.Query(db.rebind(query), whereBindings...)
}

func (db *SQLDB) upsert(logger lager.Logger, q Queryable, table string, keyAttributes, updateAttributes SQLAttributes) (sql.Result, error) {
	columns := make([]string, 0, len(keyAttributes)+len(updateAttributes))
	keyNames := make([]string, 0, len(keyAttributes))
	updateBindings := make([]string, 0, len(updateAttributes))
	bindingValues := make([]interface{}, 0, len(keyAttributes)+2*len(updateAttributes))

	keyBindingValues := make([]interface{}, 0, len(keyAttributes))
	nonKeyBindingValues := make([]interface{}, 0, len(updateAttributes))

	for column, value := range keyAttributes {
		columns = append(columns, column)
		keyNames = append(keyNames, column)
		keyBindingValues = append(keyBindingValues, value)
	}

	for column, value := range updateAttributes {
		columns = append(columns, column)
		updateBindings = append(updateBindings, fmt.Sprintf("%s = ?", column))
		nonKeyBindingValues = append(nonKeyBindingValues, value)
	}

	insertBindings := questionMarks(len(keyAttributes) + len(updateAttributes))

	var query string
	switch db.flavor {
	case Postgres:
		bindingValues = append(bindingValues, nonKeyBindingValues...)
		bindingValues = append(bindingValues, keyBindingValues...)
		bindingValues = append(bindingValues, keyBindingValues...)
		bindingValues = append(bindingValues, nonKeyBindingValues...)

		insert := fmt.Sprintf(`
				INSERT INTO %s
					(%s)
				SELECT %s`,
			table,
			strings.Join(columns, ", "),
			insertBindings)

		// TODO: Add where clause with key values.
		// Alternatively upgrade to postgres 9.5 :D
		whereClause := []string{}
		for _, key := range keyNames {
			whereClause = append(whereClause, fmt.Sprintf("%s = ?", key))
		}

		upsert := fmt.Sprintf(`
				UPDATE %s SET
					%s
				WHERE %s
				`,
			table,
			strings.Join(updateBindings, ", "),
			strings.Join(whereClause, " AND "),
		)

		query = fmt.Sprintf(`
				WITH upsert AS (%s RETURNING *)
				%s WHERE NOT EXISTS
				(SELECT * FROM upsert)
				`,
			upsert,
			insert)

		result, err := q.Exec(fmt.Sprintf("LOCK TABLE %s IN SHARE ROW EXCLUSIVE MODE", table))
		if err != nil {
			return result, err
		}

	case MySQL:
		bindingValues = append(bindingValues, keyBindingValues...)
		bindingValues = append(bindingValues, nonKeyBindingValues...)
		bindingValues = append(bindingValues, nonKeyBindingValues...)

		query = fmt.Sprintf(`
				INSERT INTO %s
					(%s)
				VALUES (%s)
				ON DUPLICATE KEY UPDATE
					%s
			`,
			table,
			strings.Join(columns, ", "),
			insertBindings,
			strings.Join(updateBindings, ", "),
		)
	case MSSQL:
		bindingValues = append(bindingValues, nonKeyBindingValues...)
		bindingValues = append(bindingValues, keyBindingValues...)
		bindingValues = append(bindingValues, keyBindingValues...)
		bindingValues = append(bindingValues, nonKeyBindingValues...)

		insert := fmt.Sprintf(`
				INSERT INTO %s
					(%s)
				VALUES(%s)`,
			table,
			strings.Join(columns, ", "),
			insertBindings)

		whereClause := []string{}
		for _, key := range keyNames {
			whereClause = append(whereClause, fmt.Sprintf("%s = ?", key))
		}

		upsert := fmt.Sprintf(`
				UPDATE %s SET
					%s
				WHERE %s
				`,
			table,
			strings.Join(updateBindings, ", "),
			strings.Join(whereClause, " AND "),
		)

		query = fmt.Sprintf(`
				%s
				if @@ROWCOUNT = 0
				  %s
				`,
			upsert,
			insert)

	default:
		// totally shouldn't happen
		panic("database flavor not implemented: " + db.flavor)
	}
	return q.Exec(db.rebind(query), bindingValues...)
}

// INSERT INTO <table> (...) VALUES ...
func (db *SQLDB) insert(logger lager.Logger, q Queryable, table string, attributes SQLAttributes) (sql.Result, error) {
	attributeCount := len(attributes)
	if attributeCount == 0 {
		return nil, nil
	}

	query := fmt.Sprintf("INSERT INTO %s\n", table)
	attributeNames := make([]string, 0, attributeCount)
	attributeBindings := make([]string, 0, attributeCount)
	bindings := make([]interface{}, 0, attributeCount)

	for column, value := range attributes {
		attributeNames = append(attributeNames, column)
		attributeBindings = append(attributeBindings, "?")
		bindings = append(bindings, value)
	}
	query += fmt.Sprintf("(%s)", strings.Join(attributeNames, ", "))
	query += fmt.Sprintf("VALUES (%s)", strings.Join(attributeBindings, ", "))

	return q.Exec(db.rebind(query), bindings...)
}

// UPDATE <table> SET ... WHERE ...
func (db *SQLDB) update(logger lager.Logger, q Queryable, table string, updates SQLAttributes, wheres string, whereBindings ...interface{}) (sql.Result, error) {
	updateCount := len(updates)
	if updateCount == 0 {
		return nil, nil
	}

	query := fmt.Sprintf("UPDATE %s SET\n", table)
	updateQueries := make([]string, 0, updateCount)
	bindings := make([]interface{}, 0, updateCount+len(whereBindings))

	for column, value := range updates {
		updateQueries = append(updateQueries, fmt.Sprintf("%s = ?", column))
		bindings = append(bindings, value)
	}
	query += strings.Join(updateQueries, ", ") + "\n"
	if len(wheres) > 0 {
		query += "WHERE " + wheres
		bindings = append(bindings, whereBindings...)
	}

	return q.Exec(db.rebind(query), bindings...)
}

// DELETE FROM <table> WHERE ...
func (db *SQLDB) delete(logger lager.Logger, q Queryable, table string, wheres string, whereBindings ...interface{}) (sql.Result, error) {
	query := fmt.Sprintf("DELETE FROM %s\n", table)

	if len(wheres) > 0 {
		query += "WHERE " + wheres
	}

	return q.Exec(db.rebind(query), whereBindings...)
}

func (db *SQLDB) rebind(query string) string {
	return RebindForFlavor(query, db.flavor)
}

func questionMarks(count int) string {
	if count == 0 {
		return ""
	}
	return strings.Repeat("?, ", count-1) + "?"
}
