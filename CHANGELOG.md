# Changelog

## 1.0.0 (2026-08-29)


### Features

* add AES-256-GCM secret encryption ([12a6ad8](https://github.com/jullury/mitia-ops/commit/12a6ad8764ff1e1b86f498f2e5c35566031ba197))
* add docker compose control wrapper ([bd09aa1](https://github.com/jullury/mitia-ops/commit/bd09aa15fb3355335ad855e607c0212444210e0c))
* add render package for compose and ephemeral env generation ([1eb6477](https://github.com/jullury/mitia-ops/commit/1eb6477e7075141dd586a69c18b9d05a8cad989f))
* add service registry with typed field definitions ([f588d8e](https://github.com/jullury/mitia-ops/commit/f588d8ebda99ba71f9cc87dba6638ed52e6303b3))
* add sqlite db layer with services and config items ([29420d6](https://github.com/jullury/mitia-ops/commit/29420d6d028c7349c34517a9cddbba9b9427244d))
* add web server with embedded templates and docker actions ([c52f531](https://github.com/jullury/mitia-ops/commit/c52f531137dcf6383f782ebe5b4f612d33988c25))
* **cloudflared:** auto-login, tunnel provisioning, DNS routing and config reload ([0a17b6e](https://github.com/jullury/mitia-ops/commit/0a17b6e10e14f5ad89e41c4855b2bd7c20a4f698))
* **docker:** raw runner and volume/size helpers ([dd023fa](https://github.com/jullury/mitia-ops/commit/dd023fa08de2ed487e63d220fd36250ee6168d8f))
* implement concrete per-service renderers ([64959af](https://github.com/jullury/mitia-ops/commit/64959afb23c4de999850be89c17d5fd62c45aebf))
* **release:** auto-versioned releases + binary-fetch installer ([021d47a](https://github.com/jullury/mitia-ops/commit/021d47a02f73befa2f3378a5c24293b2437e9d74))
* **server:** wire raw Docker runner; docs and ignore ([b96c618](https://github.com/jullury/mitia-ops/commit/b96c61853b2a54dd19835ae64c57bc63c6c4e653))
* **services:** add ReadOnly and ConfigURL capability to Definition ([8dffd6d](https://github.com/jullury/mitia-ops/commit/8dffd6d421296c20659a84a03b8f3d1faa552b9a))
* **services:** advertise minio server and console URLs ([981372e](https://github.com/jullury/mitia-ops/commit/981372e8d4e9f1171c0587bd14fc20eeedbea113))
* **services:** mailcow as read-only link-only kind with HTTP port ([fb763c5](https://github.com/jullury/mitia-ops/commit/fb763c5fd039e446de0c452f8ef231bbd0364829))
* **services:** size field, SplitSize, external named volume ([8e20790](https://github.com/jullury/mitia-ops/commit/8e20790e21bdb9a8b111534b0246c7b77b954948))
* **services:** UUID service ids with data-preserving migration ([3e2d5de](https://github.com/jullury/mitia-ops/commit/3e2d5de2a17f4bc418ad2474323f8c6ec9f82123))
* **web:** delete a service (compose down, data volume, deploy dir, DB row) ([b084d97](https://github.com/jullury/mitia-ops/commit/b084d9798076a04ccfb6cceff1ac351b5068fb2c))
* **web:** read-only service lifecycle rejection and mailcow status path ([0cca605](https://github.com/jullury/mitia-ops/commit/0cca605555d3d6e09bbc6aa9a133238a27308415))
* **web:** redesign service UI; parse real container status ([9574d53](https://github.com/jullury/mitia-ops/commit/9574d53a941fa30586bc2977515cb0a0b2f4cc3e))
* **web:** render open-config link for read-only mailcow rows ([811d195](https://github.com/jullury/mitia-ops/commit/811d195fca617686e7490151941698e22c0c9ff2))
* **web:** size picker, save-as-resize, preflight, inline errors, UI ([79e348d](https://github.com/jullury/mitia-ops/commit/79e348d306977f9addd1e44927a744257024d8df))
* **web:** start-on-boot, mailcow conf reconciliation, systemd install ([87e12f1](https://github.com/jullury/mitia-ops/commit/87e12f149fcb200592bacf2304055f394763a955))


### Bug Fixes

* **docker:** drop dead composeCmd helper; single TrimSpace ([706c3b4](https://github.com/jullury/mitia-ops/commit/706c3b44a2e085337a911f4ff35c7c36a03722e6))
* **docker:** plain local data volumes; size is advisory ([4731136](https://github.com/jullury/mitia-ops/commit/473113621d888d66534be3dce20a9602259beead))
* **render:** only fall back to dotenv when a compose payload exists ([0dc7ac2](https://github.com/jullury/mitia-ops/commit/0dc7ac2599d6cb42ad0d08645e8e30ce39d8d030))
* **render:** quote and escape unsafe DotEnv values, refuse newlines ([2577278](https://github.com/jullury/mitia-ops/commit/257727892eda2435064f6f1d40baacdf896bab95))
* **services:** remove unused MINIO_BROWSER field ([d67e844](https://github.com/jullury/mitia-ops/commit/d67e8441616c6e9acc9e286e06be16d00257d32a))
* **web:** 400 on unknown action op; remove dead no-ops; strengthen dashboard test ([02b5386](https://github.com/jullury/mitia-ops/commit/02b5386dbe6a7226778156a94da613585396bfdc))
* **web:** harden read-only mailcow lifecycle guard and skip empty deploy dir ([781b104](https://github.com/jullury/mitia-ops/commit/781b104ac5d0d023b63cf14c9d60a2ea0ef6ef1d))
* **web:** right-align services list action buttons ([5114ca4](https://github.com/jullury/mitia-ops/commit/5114ca49313faee1e215d0d359c6033d6f6370d8))
