package gosearcher

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
)

var errVersionUnavailable = errors.New("gosearcher: database change version unavailable")

// driverConnector opens an independent connection to the same file through
// the caller's driver. It avoids reserving a connection from the caller's pool.
type driverConnector struct {
	driver driver.Driver
	dsn    string
}

func (c driverConnector) Connect(ctx context.Context) (driver.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return c.driver.Open(c.dsn)
}

func (c driverConnector) Driver() driver.Driver { return c.driver }

// Version compares SQLite's data_version on one stable monitoring connection.
// For non-file databases or unsupported drivers, caching is disabled.
func (q *sqlQuerier) Version(ctx context.Context) (int64, error) {
	q.monitorMu.Lock()
	defer q.monitorMu.Unlock()
	if q.versionUnavailable {
		return 0, errVersionUnavailable
	}
	if q.monitorConn == nil {
		file, err := q.databaseFile(ctx)
		if err != nil || file == "" {
			if ctx.Err() != nil {
				return 0, ctx.Err()
			}
			q.versionUnavailable = true
			return 0, errVersionUnavailable
		}
		connector := driverConnector{driver: q.db.Driver(), dsn: file}
		if d, ok := q.db.Driver().(driver.DriverContext); ok {
			if native, err := d.OpenConnector(file); err == nil {
				q.monitorDB = sql.OpenDB(native)
			} else {
				q.versionUnavailable = true
				return 0, errVersionUnavailable
			}
		} else {
			q.monitorDB = sql.OpenDB(connector)
		}
		q.monitorDB.SetMaxOpenConns(1)
		q.monitorConn, err = q.monitorDB.Conn(ctx)
		if err != nil {
			_ = q.closeMonitorLocked()
			if ctx.Err() != nil {
				return 0, ctx.Err()
			}
			q.versionUnavailable = true
			return 0, errVersionUnavailable
		}
	}
	var version int64
	if err := q.monitorConn.QueryRowContext(ctx, "PRAGMA data_version").Scan(&version); err != nil {
		_ = q.closeMonitorLocked()
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		q.versionUnavailable = true
		return 0, errVersionUnavailable
	}
	return version, nil
}

func (q *sqlQuerier) databaseFile(ctx context.Context) (string, error) {
	rows, err := q.db.QueryContext(ctx, "PRAGMA database_list")
	if err != nil {
		return "", err
	}
	defer rows.Close()
	for rows.Next() {
		var seq int
		var name, file string
		if err := rows.Scan(&seq, &name, &file); err != nil {
			return "", err
		}
		if name == "main" {
			return file, nil
		}
	}
	return "", rows.Err()
}

func (q *sqlQuerier) closeMonitor() error {
	q.monitorMu.Lock()
	defer q.monitorMu.Unlock()
	q.versionUnavailable = true
	return q.closeMonitorLocked()
}

func (q *sqlQuerier) closeMonitorLocked() error {
	var err error
	if q.monitorConn != nil {
		err = q.monitorConn.Close()
		q.monitorConn = nil
	}
	if q.monitorDB != nil {
		if closeErr := q.monitorDB.Close(); err == nil {
			err = closeErr
		}
		q.monitorDB = nil
	}
	return err
}
