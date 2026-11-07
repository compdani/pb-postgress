package validators

import (
	"database/sql"
	"errors"
	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
	"strings"

	"github.com/pocketbase/dbx"
	validation "github.com/pocketbase/ozzo-validation/v4"
)

// UniqueId checks whether a field string id already exists in the specified table.
//
// Example:
//
//	validation.Field(&form.RelId, validation.By(validators.UniqueId(form.app.DB(), "tbl_example"))
func UniqueId(db dbx.Builder, tableName string) validation.RuleFunc {
	return func(value any) error {
		v, _ := value.(string)
		if v == "" {
			return nil // nothing to check
		}

		var foundId string

		err := db.
			Select("id").
			From(tableName).
			Where(dbx.HashExp{"id": v}).
			Limit(1).
			Row(&foundId)

		if (err != nil && !errors.Is(err, sql.ErrNoRows)) || foundId != "" {
			return validation.NewError("validation_invalid_or_existing_id", "The model id is invalid or already exists.")
		}

		return nil
	}
}

// NormalizeUniqueIndexError attempts to convert a
// "unique constraint failed" error into a validation.Errors.
//
// The provided err is returned as it is without changes if:
// - err is nil
// - err is already validation.Errors
// - err is not "unique constraint failed" error
func NormalizeUniqueIndexError(err error, tableOrAlias string, fieldNames []string) error {
	if err == nil {
		return err
	}

	if _, ok := err.(validation.Errors); ok {
		return err
	}

	var pg *pgconn.PgError
	var my *mysql.MySQLError
	var columns string
	if errors.As(err, &pg) && pg.Code == "23505" {
		columns = pg.Detail
	}
	if errors.As(err, &my) && my.Number == 1062 {
		columns = my.Message
	}
	if columns != "" {
		normalized := validation.Errors{}
		for _, name := range fieldNames {
			if strings.Contains(columns, "("+name+")") || strings.Contains(columns, "_"+name+"_") || strings.Contains(columns, "."+name+"'") {
				normalized[name] = validation.NewError("validation_not_unique", "Value must be unique")
			}
		}
		if len(normalized) > 0 {
			return normalized
		}
	}
	msg := strings.ToLower(err.Error())

	// check for unique constraint failure
	if strings.Contains(msg, "unique constraint failed") {
		// note: extra space to unify multi-columns lookup
		msg = strings.ReplaceAll(strings.TrimSpace(msg), ",", " ") + " "

		normalizedErrs := validation.Errors{}

		for _, name := range fieldNames {
			// note: extra spaces to exclude table name with suffix matching the current one
			// 		 OR other fields starting with the current field name
			if strings.Contains(msg, strings.ToLower(" "+tableOrAlias+"."+name+" ")) {
				normalizedErrs[name] = validation.NewError("validation_not_unique", "Value must be unique")
			}
		}

		if len(normalizedErrs) > 0 {
			return normalizedErrs
		}
	}

	return err
}
