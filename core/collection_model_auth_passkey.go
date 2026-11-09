package core

import (
	"net/url"

	validation "github.com/pocketbase/ozzo-validation/v4"
	"github.com/pocketbase/ozzo-validation/v4/is"
)

// PasskeyConfig defines the options for the usernameless passkey
// (WebAuthn discoverable credential) authentication.
//
// The user is not asked for an email or username - the authenticator
// returns the stored user handle and the record is resolved from it.
type PasskeyConfig struct {
	Enabled bool `form:"enabled" json:"enabled"`

	// RPID is the WebAuthn relying party id (a registrable domain, eg. "example.com").
	//
	// Passkeys are bound to it, so changing it invalidates all existing passkeys.
	// Leave empty to use the hostname of the application URL setting.
	RPID string `form:"rpId" json:"rpId"`

	// RPName is the relying party name shown by some authenticators.
	// Leave empty to use the application name setting.
	RPName string `form:"rpName" json:"rpName"`

	// Origins lists the allowed origins of the WebAuthn ceremonies
	// (eg. "https://app.example.com"). Leave empty to use the
	// origin of the application URL setting.
	Origins []string `form:"origins" json:"origins"`
}

// Validate makes PasskeyConfig validatable by implementing [validation.Validatable] interface.
func (c PasskeyConfig) Validate() error {
	if !c.Enabled {
		return nil
	}

	return validation.ValidateStruct(&c,
		validation.Field(&c.RPID, validation.When(c.RPID != "localhost", is.Domain)),
		validation.Field(&c.RPName, validation.Length(0, 255)),
		validation.Field(&c.Origins, validation.Each(validation.By(checkPasskeyOrigin))),
	)
}

func checkPasskeyOrigin(value any) error {
	raw, _ := value.(string)

	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.Path != "" || u.RawQuery != "" {
		return validation.NewError("validation_invalid_origin", "Must be an origin like https://app.example.com.")
	}

	if u.Scheme == "http" && !isLoopbackHost(u.Hostname()) {
		return validation.NewError("validation_insecure_origin", "Plain http is allowed only for localhost.")
	}

	return nil
}
