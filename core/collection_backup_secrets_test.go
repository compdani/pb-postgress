package core

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCollectionsBackupJSONKeepsSecrets(t *testing.T) {
	t.Parallel()

	c := NewAuthCollection("users")
	c.AuthToken.Secret = "auth_token_secret_0123456789abcdefghij"
	c.OAuth2.Providers = []OAuth2ProviderConfig{{Name: "google", ClientId: "id", ClientSecret: "provider_secret_xyz"}}
	c.OAuth2Server.Clients = []OAuth2ServerClient{{Id: "app", Secret: "oauth2_server_client_secret_0123456789"}}

	regular, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}

	backup, err := collectionsBackupJSON([]*Collection{c})
	if err != nil {
		t.Fatal(err)
	}

	for _, secret := range []string{c.AuthToken.Secret, "provider_secret_xyz", "oauth2_server_client_secret_0123456789"} {
		if strings.Contains(string(regular), secret) {
			t.Fatalf("the regular serialization must not expose %q", secret)
		}
		if !strings.Contains(string(backup[0]), secret) {
			t.Fatalf("the backup serialization must keep %q", secret)
		}
	}

	// the backup json is a valid import payload
	imported := &Collection{}
	if err := json.Unmarshal(backup[0], imported); err != nil {
		t.Fatal(err)
	}
	if imported.OAuth2.Providers[0].ClientSecret != "provider_secret_xyz" || imported.AuthToken.Secret != c.AuthToken.Secret {
		t.Fatalf("secrets lost after the import round trip")
	}
}
