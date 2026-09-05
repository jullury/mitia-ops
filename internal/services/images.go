package services

// Pinned container images for each service kind. Keeping them in one file
// makes upgrades deliberate: bump the tag here, re-release, and the next
// deploy pulls a reproducible image instead of whatever :latest resolves to.
//
// To update:
//  1. pick the latest stable tag from Docker Hub / upstream releases
//  2. bump the constant below
//  3. re-release — existing deploys recreate the container on the next Start
//
// Cloudflared has its own constant in internal/cloudflared (CloudflaredImage)
// because both the CLI runner and the compose file reference it.
const (
	garageImage    = "dxflrs/garage:v2.3.0"
	vaultImage     = "hashicorp/vault:2.0.4"
	caddyImage     = "caddy:2.11.4"
	postgresImage  = "postgres:16-alpine"
	glitchTipImage = "glitchtip/glitchtip:6.2.6"
	valkeyImage    = "valkey/valkey:9.0.6-alpine"
)
