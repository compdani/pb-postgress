package search

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/ganigeorgiev/fexpr"
	"github.com/pocketbase/dbx"
)

// TokenFunctionsProvider allows field resolvers to supply dialect-specific token functions.
type TokenFunctionsProvider interface {
	TokenFunctionsMap() map[string]func(
		argTokenResolverFunc func(fexpr.Token) (*ResolverResult, error),
		args ...fexpr.Token,
	) (*ResolverResult, error)
}

func tokenFunctionsForResolver(fieldResolver FieldResolver) map[string]func(
	argTokenResolverFunc func(fexpr.Token) (*ResolverResult, error),
	args ...fexpr.Token,
) (*ResolverResult, error) {
	if provider, ok := fieldResolver.(TokenFunctionsProvider); ok {
		return provider.TokenFunctionsMap()
	}
	return TokenFunctions
}

// PostgresTokenFunctions contains PostgreSQL-specific filter function equivalents.
var PostgresTokenFunctions = map[string]func(
	argTokenResolverFunc func(fexpr.Token) (*ResolverResult, error),
	args ...fexpr.Token,
) (*ResolverResult, error){
	"geoDistance": TokenFunctions["geoDistance"],
	"strftime":    postgresStrftime,
}

func postgresStrftime(argTokenResolverFunc func(fexpr.Token) (*ResolverResult, error), args ...fexpr.Token) (*ResolverResult, error) {
	totalArgs := len(args)

	if totalArgs < 1 {
		return nil, fmt.Errorf("[strftime] expected at least 1 arguments, got %d", len(args))
	}

	if totalArgs > 10 {
		return nil, fmt.Errorf("[strftime] too many arguments (max allowed 10, got %d)", totalArgs)
	}

	if args[0].Type != fexpr.TokenText {
		return nil, errors.New("[strftime] expects the first argument to be a format string")
	}

	formatArgResult, err := argTokenResolverFunc(args[0])
	if err != nil {
		return nil, fmt.Errorf("[strftime] failed to resolve format argument: %w", err)
	}

	pgFormat := sqliteToPostgresDateFormat(formatArgResult.Identifier)

	if totalArgs == 1 {
		formatArgResult.NullFallback = NullFallbackEnforced
		formatArgResult.Identifier = "to_char(CURRENT_TIMESTAMP, " + pgFormat + ")"
		return formatArgResult, nil
	}

	allowedTimeValueTokens := []fexpr.TokenType{fexpr.TokenText, fexpr.TokenIdentifier, fexpr.TokenNumber}
	if !slices.Contains(allowedTimeValueTokens, args[1].Type) {
		return nil, errors.New("[strftime] expects the second argument to be of a valid time-value type")
	}

	timeValueArgResult, err := argTokenResolverFunc(args[1])
	if err != nil {
		return nil, fmt.Errorf("[strftime] failed to resolve time-value argument: %w", err)
	}

	resolvedModifierArgs := make([]*ResolverResult, totalArgs-2)
	for i, arg := range args[2:] {
		if arg.Type != fexpr.TokenText {
			return nil, fmt.Errorf("[strftime] invalid modifier argument %d - can be only string", i)
		}

		resolved, err := argTokenResolverFunc(arg)
		if err != nil {
			return nil, fmt.Errorf("[strftime] failed to resolve modifier argument %d: %w", i, err)
		}

		resolvedModifierArgs[i] = resolved
	}

	result := &ResolverResult{
		NullFallback: NullFallbackEnforced,
		Params:       dbx.Params{},
	}

	timeExpr := "to_timestamp(" + timeValueArgResult.Identifier + ", 'YYYY-MM-DD HH24:MI:SS')"
	if err = concatUniqueParams(result.Params, formatArgResult.Params); err != nil {
		return nil, err
	}
	if err = concatUniqueParams(result.Params, timeValueArgResult.Params); err != nil {
		return nil, err
	}
	for _, m := range resolvedModifierArgs {
		if err = concatUniqueParams(result.Params, m.Params); err != nil {
			return nil, err
		}
	}

	result.Identifier = "to_char((" + timeExpr + ")::timestamptz, " + pgFormat + ")"

	if timeValueArgResult.MultiMatchSubQuery != nil {
		timeExpr = "to_timestamp(" + timeValueArgResult.MultiMatchSubQuery.ValueIdentifier + ", 'YYYY-MM-DD HH24:MI:SS')"
		result.MultiMatchSubQuery = timeValueArgResult.MultiMatchSubQuery
		result.MultiMatchSubQuery.ValueIdentifier = "to_char((" + timeExpr + ")::timestamptz, " + pgFormat + ")"

		err = concatUniqueParams(result.MultiMatchSubQuery.Params, result.Params)
		if err != nil {
			return nil, err
		}
	}

	return result, nil
}

func sqliteToPostgresDateFormat(sqliteFormat string) string {
	replacements := []struct{ from, to string }{
		{"%Y", "YYYY"},
		{"%y", "YY"},
		{"%m", "MM"},
		{"%d", "DD"},
		{"%H", "HH24"},
		{"%M", "MI"},
		{"%S", "SS"},
		{"%W", "IW"},
		{"%w", "D"},
		{"%j", "DDD"},
		{"%f", "US"},
	}

	result := sqliteFormat
	for _, r := range replacements {
		result = strings.ReplaceAll(result, r.from, r.to)
	}

	return result
}
