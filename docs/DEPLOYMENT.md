# Deploy di produzione (fork Haxurus)

YAGPDB segue il modello di Sentinel, Muse e Dispatch: l'immagine viene costruita su GitHub Actions, pubblicata su GHCR e installata sulla VPS tramite digest SHA-256 immutabile, attraverso un account SSH ristretto. Sulla VPS non si esegue nessuna build e nessun `git pull` come root. I segreti restano sulla VPS e non transitano mai da GitHub.

```text
push main ─> CI (test sui soli package toccati) ─> build deploy/Dockerfile (SBOM, provenance) ─> GHCR
          ─> SSH yagpdb-deploy (forced command) ─> sudo yagpdb-deploy deploy ghcr.io/haxurus/yagpdb@sha256:…
             ─> backup PostgreSQL + Redis ─> pull per digest ─> config dal bundle dell'immagine
             ─> postgres/redis ─> db-init ─> yagpdb ─> edge ─> healthcheck
             ─> ripristino automatico della release precedente se i healthcheck falliscono
```

Riferimenti di questa guida: dominio `yagpdb.haxurus.com`, VPS01 (Debian 13, Docker CE, Nginx Proxy Manager sulla rete esterna `proxy_net`), Cloudflare in modalità proxy con SSL Full (strict).

Differenze rispetto a Dispatch:

- **Configurazione dentro l'immagine.** Compose, `nginx.conf` e `db-init.sh` vengono estratti da `/opt/yagpdb-deploy` dell'immagine deployata (come Muse). Una modifica a `deploy/` arriva sulla VPS con il deploy successivo, senza rieseguire l'installer; un rollback ripristina anche la configurazione della release precedente. L'installer va rieseguito solo quando cambiano `ops/` o `security/`.
- **Un solo processo.** YAGPDB gira con `-all` (bot, pannello web, feed e background worker nello stesso container). Le migrazioni dello schema le esegue YAGPDB all'avvio.
- **Redis contiene dati, non solo cache.** Gran parte delle impostazioni dei plugin è salvata in Redis: è persistente (AOF), senza eviction, e viene salvato nei backup insieme a PostgreSQL.

## 1. Prerequisiti

### Applicazione Discord

Nel Developer Portal, una nuova applicazione dedicata (es. `YAGPDB Haxurus`):

- **Bot > Privileged Gateway Intents**: Presence, Server Members e Message Content **tutti attivi** (YAGPDB li richiede all'avvio: `bot/bot.go`).
- **OAuth2 > Redirects**: `https://yagpdb.haxurus.com/confirm_login` e `https://yagpdb.haxurus.com/manage`.
- **Installation**: disattivare "Public Bot" finché il bot resta privato.
- Annotare l'**Application ID** (`YAGPDB_CLIENTID`) e il proprio **User ID** Discord (`YAGPDB_OWNER`). Bot Token e Client Secret vanno **solo** nei file secret sulla VPS (sezione 3).

### DNS e Cloudflare

- Record `A` `yagpdb` verso l'IPv4 della VPS, **Proxied**. Nessun `AAAA` diretto.
- SSL/TLS **Full (strict)**: NPM serve un certificato valido per `yagpdb.haxurus.com` (Origin `*.haxurus.com`).

### VPS

- Docker CE con plugin Compose, `openssl`, `sudo`/`visudo`, `flock`.
- Rete Docker esterna `proxy_net` già esistente, condivisa con NPM.
- `user007` resta fuori dal gruppo `docker`: tutti i comandi usano `sudo`.

### Accesso a GHCR

La VPS scarica `ghcr.io/haxurus/yagpdb@sha256:...`. Due possibilità:

- rendere **public** il package `yagpdb` (il repository è pubblico e l'immagine non contiene segreti): `https://github.com/users/haxurus/packages/container/yagpdb/settings`;
- oppure tenerlo privato ed eseguire una volta, come root, `sudo docker login ghcr.io -u haxurus` con un token *classic* con il solo scope `read:packages`.

## 2. Installazione sulla VPS

### Chiave di deploy dedicata

Su una macchina fidata, senza riusare la chiave admin:

```bash
ssh-keygen -t ed25519 -a 100 -f ~/.ssh/yagpdb_deploy -C "yagpdb-github-actions"
```

La privata va nel secret GitHub `VPS_DEPLOY_KEY`; la pubblica va copiata sulla VPS e deve contenere **una sola riga**.

### AllowUsers

Aggiungere `yagpdb-deploy` **in fondo** alla riga `AllowUsers` esistente in `/etc/ssh/sshd_config.d/00-hardening.conf`, senza sostituirla, **prima** dell'installer (che altrimenti si ferma):

```bash
sudo sshd -T | grep '^allowusers'
sudoedit /etc/ssh/sshd_config.d/00-hardening.conf
sudo sshd -t && sudo systemctl reload ssh && sudo sshd -T | grep '^allowusers'
```

Verificare da un secondo terminale che `user007` entri ancora prima di chiudere la sessione.

### Installer

```bash
git clone --branch main https://github.com/haxurus/yagpdb.git /tmp/yagpdb
cd /tmp/yagpdb
sudo ./ops/install-vps.sh ~/yagpdb_deploy.pub
```

L'installer (idempotente, si può rieseguire):

- crea l'utente di sistema `yagpdb-deploy` (home, `.ssh` e `authorized_keys` di root) con `restrict,command="/usr/local/libexec/yagpdb-deploy-entrypoint"`;
- installa `/etc/sudoers.d/yagpdb-deploy` (solo `/usr/local/sbin/yagpdb-deploy`, senza `setenv`);
- crea `/srv/docker/yagpdb/` (root, 700) con `secrets/`, `secrets/app/`, `runtime/`, `backups/`, `logs/` e `yagpdb.env` (dal template, solo se manca);
- genera `secrets/postgres_admin_password`, `secrets/app/YAGPDB_PQPASSWORD` e `secrets/app/YAGPDB_YOUTUBE_VERIFY_TOKEN`; crea vuoti `YAGPDB_BOTTOKEN` e `YAGPDB_CLIENTSECRET`;
- installa e abilita `yagpdb-firewall.service`: il bridge `yagpdb-eg` non può raggiungere l'host né reti private/LAN;
- verifica `proxy_net`, Docker Compose e `flock`.

Per aggiornare script e firewall dopo modifiche a `ops/` o `security/`: `cd /tmp/yagpdb && git pull --ff-only && sudo ./ops/install-vps.sh`.

## 3. Configurazione

### `yagpdb.env`

```bash
sudoedit /srv/docker/yagpdb/yagpdb.env
```

Valori obbligatori: `YAGPDB_HOST=yagpdb.haxurus.com`, `YAGPDB_CLIENTID`, `YAGPDB_OWNER`. Il template (`deploy/yagpdb.env.example`) commenta anche le opzioni utili per un bot privato, tra cui `YAGPDB_PREMIUM_ALL_GUILDS_PREMIUM=true` (sblocca i limiti premium su tutti i server dove è installato). Database, Redis, porte e header del proxy sono fissati nel Compose e non vanno messi qui.

I valori salvati dal pannello proprietario (`/admin/config`) finiscono in Redis e **hanno la precedenza** su `yagpdb.env`. Le opzioni di YAGPDB vengono lette all'avvio: dopo una modifica serve un riavvio (o un deploy).

### Segreti

In `/srv/docker/yagpdb/secrets/app/` (root:10001, 750) ogni file si chiama come la variabile che rappresenta; i file vuoti vengono ignorati, quindi le integrazioni opzionali restano spente finché il loro file è vuoto.

| File | Origine | Note |
|---|---|---|
| `YAGPDB_BOTTOKEN` | **manuale** | token del bot, senza il prefisso `Bot ` |
| `YAGPDB_CLIENTSECRET` | **manuale** | client secret OAuth |
| `YAGPDB_PQPASSWORD` | generato | applicato al ruolo `yagpdb` da `db-init` a ogni deploy |
| `YAGPDB_YOUTUBE_VERIFY_TOKEN` | generato | non cambiarlo dopo aver attivato feed YouTube |
| `YAGPDB_TWITCH_CLIENTSECRET`, `YAGPDB_REDDIT_CLIENTSECRET`, `YAGPDB_REDDIT_REFRESHTOKEN`, `YAGPDB_GOOGLE_RECAPTCHA_SECRET`, `YAGPDB_GOOGLE_SAFEBROWSING_API_KEY`, `YAGPDB_SENTRY_DSN` | opzionali | crearli solo se si usa l'integrazione |
| `google-credentials.json` | opzionale | credenziali Google per YouTube, passate come `GOOGLE_APPLICATION_CREDENTIALS` |

`secrets/postgres_admin_password` (root 600) è la password del superuser PostgreSQL: la usano solo il container `postgres`, il job `db-init` e i backup. YAGPDB si collega con il ruolo non privilegiato `yagpdb`, proprietario del database `yagpdb`.

```bash
sudoedit /srv/docker/yagpdb/secrets/app/YAGPDB_BOTTOKEN
sudoedit /srv/docker/yagpdb/secrets/app/YAGPDB_CLIENTSECRET
# per un segreto opzionale nuovo:
sudo install -o root -g 10001 -m 640 /dev/null /srv/docker/yagpdb/secrets/app/YAGPDB_TWITCH_CLIENTSECRET
sudoedit /srv/docker/yagpdb/secrets/app/YAGPDB_TWITCH_CLIENTSECRET
sudo find /srv/docker/yagpdb/secrets -type f -printf '%m %u:%g %p\n'
```

Incollare solo il valore, senza virgolette. Non incollare mai questi valori in chat, issue o log.

### Nginx Proxy Manager

Proxy Host:

- Domain `yagpdb.haxurus.com`
- Scheme `http`, Forward Hostname `yagpdb-edge`, Forward Port `8080`
- SSL: certificato valido, Force SSL, HSTS
- Websockets non necessari

L'edge ricava l'IP reale del client da `CF-Connecting-IP`, accettato solo dagli indirizzi privati Docker (NPM), e lo passa a YAGPDB come `X-Real-IP`.

## 4. GitHub

### Branch e repository

- **Default branch `main`** (Settings > General). Serve anche perché i workflow pianificati (CI settimanale, sync upstream) girano solo dal default branch.
- `master` resta lo specchio di `botlabs-gg/yagpdb` (vedi [UPSTREAM.md](UPSTREAM.md)).
- Branch protection su `main` consigliata: PR obbligatoria, check `scope`, `build`, `lint`, `deploy-config` e `test` richiesti.

### Environment `production`

Settings > Environments > `production`:

- **Deployment branches: solo `main`.**
- Required reviewers consigliati.
- Secrets **dell'environment**: `VPS_DEPLOY_KEY` (privata `yagpdb_deploy`), `VPS_KNOWN_HOSTS` (host key verificata a mano con `ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub` sulla VPS), `VPS_HOST`, `VPS_PORT` (default 22).

### Abilitazione

Variable **di repository** `ENABLE_VPS_DEPLOY=true`, da impostare solo dopo aver verificato SSH, segreti, `yagpdb.env`, accesso GHCR e NPM. Finché manca, le immagini vengono costruite e pubblicate ma il job `deploy` viene saltato.

```bash
ssh -i ~/.ssh/yagpdb_deploy -o IdentitiesOnly=yes yagpdb-deploy@<VPS_HOST> status
```

Atteso: `No release recorded yet.`. Qualsiasi altro comando viene rifiutato.

## 5. Deploy

Ogni push su `main` esegue:

1. **CI** sui soli package Go toccati dal push e su quelli che ne dipendono (vedi sezione 8); il resto è già stato verificato. Un avvio manuale del workflow testa tutto.
2. Build di `deploy/Dockerfile` (`linux/amd64`, SBOM, provenance), push su GHCR, esportazione del digest.
3. Solo con `ENABLE_VPS_DEPLOY == 'true'` e ref `main`: SSH `deploy <image@sha256>`.

Sulla VPS, `yagpdb-deploy deploy`:

1. prende il lock `/run/yagpdb-deploy.lock`;
2. valida il riferimento e lo spazio su `/var/lib/docker` (sotto 5 GiB elimina le immagini YAGPDB non più necessarie, sotto 3 GiB si ferma);
3. se esiste una release corrente: backup `pg_dump -Fc` in `backups/predeploy-<UTC>.pgdump` e snapshot Redis in `backups/predeploy-<UTC>.rdb`; se un backup fallisce il deploy si interrompe; i backup più vecchi di 14 giorni vengono rimossi solo dopo un backup riuscito;
4. pull dell'immagine per digest;
5. estrae il bundle dall'immagine, lo valida con `docker compose config` e solo allora lo installa (`docker-compose.yml`, `runtime/`, `yagpdb.env.example` come riferimento);
6. `docker compose up`: postgres e redis → `db-init` (ruolo e database `yagpdb`) → yagpdb → edge;
7. attende fino a 300 s che `yagpdb` (pannello su `:5000` e API interna su `:5100`) ed `edge` siano healthy;
8. se va bene aggiorna `.release`/`.previous-release` ed elimina le immagini diverse dalla release corrente e precedente;
9. se fallisce salva i log in `/srv/docker/yagpdb/logs/deploy-failed-<UTC>.log` (root, mantenuti 30 giorni; nell'output SSH compare solo il percorso) e riavvia la release precedente con la sua configurazione.

### Stato e log

```bash
sudo /usr/local/sbin/yagpdb-deploy status
sudo sh -c 'cd /srv/docker/yagpdb && . ./.release && YAGPDB_IMAGE=$IMAGE docker compose -p yagpdb -f docker-compose.yml logs --tail=200 yagpdb'
```

## 6. Rollback e ripristino

Workflow **Rollback production** (manuale, solo da `main`) oppure `sudo /usr/local/sbin/yagpdb-deploy rollback`: fa un backup, avvia l'immagine di `.previous-release` con la sua configurazione e scambia `.release` e `.previous-release` (un secondo rollback torna avanti). Dopo un rollback automatico (deploy fallito) `.previous-release` non cambia: controllare `status` prima di un rollback manuale.

Il rollback **non** ripristina i dati. Le migrazioni di YAGPDB sono additive (`ADD COLUMN IF NOT EXISTS` e simili), quindi la versione precedente funziona sullo schema nuovo.

### Ripristino di un backup

```bash
sudo ls -l /srv/docker/yagpdb/backups/
# 1. fermare bot ed edge (postgres e redis restano attivi)
sudo sh -c 'cd /srv/docker/yagpdb && . ./.release && YAGPDB_IMAGE=$IMAGE docker compose -p yagpdb -f docker-compose.yml stop edge yagpdb'
# 2a. PostgreSQL
sudo sh -c 'cd /srv/docker/yagpdb && . ./.release && YAGPDB_IMAGE=$IMAGE docker compose -p yagpdb -f docker-compose.yml exec -T postgres sh -c '"'"'export PGPASSWORD="$(cat /run/secrets/postgres_admin_password)"; pg_restore --clean --if-exists -U postgres -d yagpdb'"'"' < backups/predeploy-<STAMP>.pgdump'
# 2b. Redis: con redis fermo, sostituire dump.rdb e rimuovere l'AOF (redis lo ricostruisce dal dump)
sudo sh -c 'cd /srv/docker/yagpdb && . ./.release && YAGPDB_IMAGE=$IMAGE docker compose -p yagpdb -f docker-compose.yml stop redis'
sudo sh -c 'v=$(docker volume inspect -f "{{.Mountpoint}}" yagpdb_redis_data) && install -o 999 -g 1000 -m 600 /srv/docker/yagpdb/backups/predeploy-<STAMP>.rdb "$v/dump.rdb" && rm -rf "$v/appendonlydir"'
# 3. riavviare tutto
sudo sh -c 'cd /srv/docker/yagpdb && . ./.release && YAGPDB_IMAGE=$IMAGE docker compose -p yagpdb -f docker-compose.yml up -d'
```

I backup restano sulla VPS: copiarli periodicamente fuori dall'host.

## 7. Isolamento a runtime

| Servizio | Reti | Segreti |
|---|---|---|
| postgres | db | postgres_admin_password |
| redis (uid 999) | cache | nessuno |
| db-init (one-shot) | db | postgres_admin_password, YAGPDB_PQPASSWORD |
| yagpdb (uid 10001) | db, cache, frontend, egress | `secrets/app/*` |
| edge (nginx, uid 101) | frontend, proxy_net | nessuno |

- Tutte le reti tranne `egress` e `proxy_net` sono `internal`; nessuna porta è pubblicata sull'host. Sottoreti fisse `/28` in `10.251.1.0/24` (Dispatch usa `10.251.0.0/24`).
- Redis non ha password perché YAGPDB non sa inviarla: la rete `cache` collega solo redis e yagpdb.
- Container read-only, non-root (tranne i job postgres), `cap_drop: ALL`, `no-new-privileges`, limiti di pid, memoria e CPU. Nessun container monta `docker.sock`.
- Il firewall di egress copre IPv4; le reti YAGPDB non abilitano IPv6.

## 8. Test e CI

- Le PR verso `main` eseguono la CI (`.github/workflows/ci.yml`): `scope` calcola i package toccati e quelli che ne dipendono, `test` li esegue con PostgreSQL 17 e Redis 8 reali e fallisce se un package salta i test di database, `build` compila tutto, `deploy-config` valida il Compose e, se cambia `deploy/`, costruisce e prova l'immagine.
- `go.mod`, `go.sum` o gli script della CI fanno girare l'intera suite e `govulncheck`. Ogni lunedì gira comunque la suite completa.
- I test saltati restanti sono legittimi e compaiono nel riepilogo del job: quelli che richiedono chiavi esterne (safebrowsing, discordgo) e i vecchi test del check loop di `scheduledevents2`, che richiedono un bot connesso (coperti dai test di storage in `scheduledevents_db_test.go`).

## 9. Sequenza completa dei comandi

`<VPS_IP>` è l'IPv4 di VPS01; i comandi locali vanno eseguiti in Git Bash con `gh` autenticato come `haxurus`.

### A. Prima di iniziare

1. Default branch `main`, branch protection (sezione 4).
2. Discord Developer Portal: intents, redirect, Application ID (sezione 1).
3. Cloudflare: record `A` `yagpdb` → `<VPS_IP>`, Proxied.
4. GHCR: dopo il primo build su `main`, rendere pubblico il package `yagpdb` (oppure punto C.5).

### B. Macchina locale

```bash
ssh-keygen -t ed25519 -a 100 -f ~/.ssh/yagpdb_deploy -C "yagpdb-github-actions"
scp ~/.ssh/yagpdb_deploy.pub user007@<VPS_IP>:~/
```

### C. VPS (come `user007`)

```bash
# 1. AllowUsers: aggiungere yagpdb-deploy in fondo alla riga esistente
sudo sshd -T | grep '^allowusers'
sudoedit /etc/ssh/sshd_config.d/00-hardening.conf
sudo sshd -t && sudo systemctl reload ssh && sudo sshd -T | grep '^allowusers'

# 2. Installer
git clone --branch main https://github.com/haxurus/yagpdb.git /tmp/yagpdb
cd /tmp/yagpdb
sudo ./ops/install-vps.sh ~/yagpdb_deploy.pub

# 3. Configurazione
sudoedit /srv/docker/yagpdb/yagpdb.env
sudoedit /srv/docker/yagpdb/secrets/app/YAGPDB_BOTTOKEN
sudoedit /srv/docker/yagpdb/secrets/app/YAGPDB_CLIENTSECRET

# 4. Firewall e host key
sudo systemctl status yagpdb-firewall.service --no-pager
ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub
printf '%s %s\n' "<VPS_IP>" "$(cut -d' ' -f1,2 /etc/ssh/ssh_host_ed25519_key.pub)"

# 5. Solo se il package GHCR resta privato
sudo docker login ghcr.io -u haxurus
```

6. NPM: Proxy Host `yagpdb.haxurus.com` → `http` `yagpdb-edge` `8080`, certificato Origin `*.haxurus.com`, Force SSL, HSTS.

### D. GitHub (macchina locale)

```bash
gh api -X PUT repos/haxurus/yagpdb/environments/production \
  -F 'deployment_branch_policy[protected_branches]=false' \
  -F 'deployment_branch_policy[custom_branch_policies]=true'
gh api -X POST repos/haxurus/yagpdb/environments/production/deployment-branch-policies -f name=main
gh secret set VPS_DEPLOY_KEY --env production -R haxurus/yagpdb < ~/.ssh/yagpdb_deploy
gh secret set VPS_HOST --env production -R haxurus/yagpdb --body "<VPS_IP>"
gh secret set VPS_PORT --env production -R haxurus/yagpdb --body 22
gh secret set VPS_KNOWN_HOSTS --env production -R haxurus/yagpdb   # riga stampata al punto C.4
ssh -i ~/.ssh/yagpdb_deploy -o IdentitiesOnly=yes yagpdb-deploy@<VPS_IP> status
gh variable set ENABLE_VPS_DEPLOY --body true -R haxurus/yagpdb
gh workflow run "Build and deploy" -R haxurus/yagpdb --ref main
gh run watch -R haxurus/yagpdb
```

### E. Verifica

```bash
sudo /usr/local/sbin/yagpdb-deploy status
sudo docker exec npm curl -sI http://yagpdb-edge:8080/robots.txt
curl -sI https://yagpdb.haxurus.com/robots.txt
```

Poi: login su `https://yagpdb.haxurus.com`, invito del bot su un server di prova, un comando slash (`/ping`), una modifica dal pannello, `/admin` con l'account `YAGPDB_OWNER`.

### F. Operazioni successive

- Nuova versione: merge su `main` → deploy automatico.
- Aggiornamenti dall'upstream: [UPSTREAM.md](UPSTREAM.md).
- Rollback: `gh workflow run "Rollback production" -R haxurus/yagpdb --ref main`.
- Modifiche a `ops/` o `security/`: rieseguire l'installer dal checkout aggiornato.
