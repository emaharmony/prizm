// Package sqlite registers Prizm's SQLite database/sql driver.
package sqlite

import _ "modernc.org/sqlite" // CGO-free SQLite driver

// DriverName is the database/sql driver name registered by modernc.org/sqlite.
const DriverName = "sqlite"
