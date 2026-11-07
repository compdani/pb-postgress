package search

import (
	"fmt"
	"github.com/ganigeorgiev/fexpr"
	"strings"
)

// MySQLTokenFunctions provides MySQL 8 filter functions.
var MySQLTokenFunctions = map[string]func(func(fexpr.Token) (*ResolverResult, error), ...fexpr.Token) (*ResolverResult, error){
	"geoDistance": func(resolve func(fexpr.Token) (*ResolverResult, error), args ...fexpr.Token) (*ResolverResult, error) {
		result, err := TokenFunctions["geoDistance"](resolve, args...)
		if err != nil {
			return nil, err
		}
		result.Identifier = replaceGeoClamp(result.Identifier)
		return result, nil
	},
	"strftime": func(resolve func(fexpr.Token) (*ResolverResult, error), args ...fexpr.Token) (*ResolverResult, error) {
		if len(args) < 1 || len(args) > 2 {
			return nil, fmt.Errorf("strftime expects a format and optional time value; MySQL modifiers are unsupported")
		}
		format, err := resolve(args[0])
		if err != nil {
			return nil, err
		}
		value := "UTC_TIMESTAMP()"
		if len(args) == 2 {
			r, err := resolve(args[1])
			if err != nil {
				return nil, err
			}
			value = r.Identifier
		}
		format.Identifier = "DATE_FORMAT(" + value + ", " + format.Identifier + ")"
		return format, nil
	},
}

func replaceGeoClamp(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "min(1, max(-1,", "LEAST(1, GREATEST(-1,"), "min(1,max(-1,", "LEAST(1,GREATEST(-1,")
}
