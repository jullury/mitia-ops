package services

import "strings"

// Vault unseal/root-token secret key names. They are generated on first init
// (via the Vault sys/init API) and persisted encrypted in the app's config
// store, so the app can unseal the service and so a recreate of the deploy dir
// keeps the same keys the data volume was sealed against.
const (
	VaultSecretUnsealKey1 = "VAULT_UNSEAL_KEY_1"
	VaultSecretUnsealKey2 = "VAULT_UNSEAL_KEY_2"
	VaultSecretUnsealKey3 = "VAULT_UNSEAL_KEY_3"
	VaultSecretUnsealKey4 = "VAULT_UNSEAL_KEY_4"
	VaultSecretUnsealKey5 = "VAULT_UNSEAL_KEY_5"
	VaultSecretRootToken  = "VAULT_ROOT_TOKEN"
)

// VaultSecretKeys lists every app-persisted Vault secret in a deterministic
// order.
func VaultSecretKeys() []string {
	return []string{
		VaultSecretUnsealKey1, VaultSecretUnsealKey2, VaultSecretUnsealKey3,
		VaultSecretUnsealKey4, VaultSecretUnsealKey5, VaultSecretRootToken,
	}
}

func init() {
	register(Definition{
		Kind:  KindVault,
		Label: "Vault (secrets management)",
		Fields: []Field{
			{Key: "VAULT_HOSTNAME", Label: "Hostname", Type: FieldString, Placeholder: "vault.example.com", Hints: "used for the advertised api_addr; optional"},
			{Key: "VAULT_PORT", Label: "Host port", Type: FieldString, Placeholder: "8200"},
			{
				Key:   "VAULT_VOLUME_SIZE",
				Label: "Volume size limit",
				Type:  FieldSize,
				Hints: "soft upper bound; guards launch only — refuse to start when the disk can't hold it together with every other service's declared size (Docker local volumes cannot enforce a size)",
				Units: []SizeUnit{
					{Label: "MiB", Suffix: "M", Base: 1 << 20},
					{Label: "GiB", Suffix: "G", Base: 1 << 30},
					{Label: "TiB", Suffix: "T", Base: 1 << 40},
				},
			},
		},
		Render: func(values map[string]string) (RenderResult, error) {
			return RenderResult{DotEnv: vaultEnv(values), ComposeYAML: vaultCompose(values)}, nil
		},
		ConfigURL: func(values map[string]string) string {
			port := strings.TrimSpace(values["VAULT_PORT"])
			if port == "" {
				port = "8200"
			}
			return "http://localhost:" + port + "/ui"
		},
	})
}

func vaultEnv(v map[string]string) string {
	var b strings.Builder
	for _, k := range []string{"VAULT_HOSTNAME", "VAULT_PORT", "VAULT_VOLUME_SIZE"} {
		if val, ok := v[k]; ok {
			b.WriteString(k + "=" + val + "\n")
		}
	}
	return b.String()
}

func vaultCompose(v map[string]string) string {
	port := strings.TrimSpace(v["VAULT_PORT"])
	if port == "" {
		port = "8200"
	}
	return `services:
  vault:
    image: ` + vaultImage + `
    command: server
    restart: unless-stopped
    cap_add:
      - IPC_LOCK
    ports:
      - "` + port + `:8200"
    environment:
      VAULT_ADDR: http://127.0.0.1:8200
    volumes:
      - ./vault.hcl:/vault/config/vault.hcl:ro
      - vault_data:/vault/file
volumes:
  vault_data:
`
}

// VaultConfig renders the vault.hcl materialized at launch and mounted into the
// container. It uses the file storage backend on the persistent volume, a TCP
// listener on 8200, and advertises an api_addr derived from the hostname (or a
// loopback fallback) so unseal/init via the raw API work.
func VaultConfig(values map[string]string) string {
	port := strings.TrimSpace(values["VAULT_PORT"])
	if port == "" {
		port = "8200"
	}
	host := strings.TrimSpace(values["VAULT_HOSTNAME"])
	apiAddr := "http://127.0.0.1:" + port
	if host != "" {
		apiAddr = "http://" + host + ":" + port
	}
	return `storage "file" {
  path = "/vault/file"
}

listener "tcp" {
  address     = "0.0.0.0:8200"
  tls_disable = 1
}

# mlock can't allocate memory in many container runtimes/kernels; disable it so
# Vault boots instead of failing with "Failed to lock memory".
disable_mlock = true

api_addr = "` + apiAddr + `"
ui = true
`
}
