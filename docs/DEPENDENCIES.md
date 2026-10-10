# Dipendenze Go: cosa togliere, aggiornare, lasciare

Audit del 2026-10-10 su `go.mod` (upstream `v2.88.2`, 55 dipendenze dirette, Go 1.26). Uso verificato con ricerca degli import nel codice; versioni da `proxy.golang.org`; manutenzione da GitHub; vulnerabilità da `govulncheck` (job `vulncheck` della CI) e OSV.

## Regola del fork

`go.mod` e `go.sum` seguono l'upstream e **non vengono modificati nel fork**. Ogni modifica lì entra in conflitto con gli aggiornamenti dell'upstream, che hanno Dependabot attivo e aggiornano spesso. Eccezione: una vulnerabilità sfruttabile nella nostra installazione, da correggere subito nel fork e proporre anche all'upstream ([UPSTREAM.md](UPSTREAM.md)).

La CI esegue `govulncheck` a ogni cambio di `go.mod` e ogni lunedì: il riepilogo del job `vulncheck` è il punto da controllare.

## Vulnerabilità aperte

| Modulo | Problema | Sfruttabile da noi? | Decisione |
|---|---|---|---|
| `golang.org/x/net` v0.58.0 | GO-2026-6603, 6610, 6611, 6612, 6617 (HTTP/2), corrette in v0.60.0 | **No.** Il pannello YAGPDB serve solo HTTP/1.1 in chiaro dietro l'edge nginx (nessun HTTP/2 lato server raggiungibile da fuori). Lato client l'HTTP/2 parla solo con Google/Discord. La libreria standard è già corretta: l'immagine usa l'ultima patch di Go 1.26 (1.26.9). | Attendere l'aggiornamento upstream; ricontrollare con il `vulncheck` settimanale. Se l'esposizione cambia (es. HTTP/2 diretto), aggiornare subito nel fork. |
| `golang.org/x/crypto` v0.55.0 | GO-2026-6354/6355 (ssh), GO-2026-5932 (openpgp) | No: pacchetti non importati. | Nessuna azione. |
| `golang.org/x/image` v0.41.0 | 5 vulnerabilità in webp/tiff/vp8l | No: si usa solo `colornames`. | Nessuna azione. |

## Da togliere (se e quando si accetta di divergere dall'upstream)

Nessuna dipendenza è inutilizzata del tutto. Queste servono solo a codice morto o di sviluppo dentro `lib/`, oppure sono sostituibili con poche righe di libreria standard. Toglierle nel fork **non conviene**: si guadagna poco e si crea conflitto. Sono buone candidate per una PR upstream di pulizia, se mai la si volesse fare.

| Modulo | Usato solo da | Come si toglie |
|---|---|---|
| `buger/jsonparser`, `json-iterator/go` (archiviato), `mailru/easyjson` | `lib/gojay/benchmarks` | eliminare `lib/gojay/benchmarks` |
| `viant/toolbox`, `viant/assertly`, `go-errors/errors` | `lib/gojay/gojay/codegen` (copia del generatore, non usata) | eliminare `lib/gojay/gojay` |
| `golang.org/x/net/websocket` (solo quel sottopacchetto) | `lib/gojay/examples/websocket` | eliminare `lib/gojay/examples` |
| `mitchellh/cli` (archiviato), `jedib0t/go-pretty` | `lib/dshardorchestrator/cmd/dso-cli` (CLI di sviluppo) | eliminare quella CLI |
| `gin-gonic/gin` | server REST dell'orchestratore (`cmd/shardorchestrator`), che non usiamo | spostare `rest/client.go` in un pacchetto proprio: gin esce dal binario `yagpdb` (oggi è compilato dentro ma non parte mai) |
| `fatih/structs` (archiviato) | 2 chiamate in `automod/automod_web.go` | `reflect` o conversione JSON |
| `miolini/datacounter` | `web/middleware.go` | `ResponseWriter` che conta i byte, ~15 righe |
| `dpatrie/urbandictionary` (fermo dal 2015) | `stdcommands/define` | client HTTP+JSON di poche righe |
| `golang.org/x/net/context` | `logs/`, `soundboard/` | `context` della libreria standard (x/net resta comunque come indiretta) |

## Da aggiornare (stessa major, nessuna modifica al codice prevista)

Arrivano con l'upstream; nessuna è urgente.

| Modulo | Attuale → ultima | Nota |
|---|---|---|
| `golang.org/x/net` | 0.58.0 → 0.61.0 | correzioni di sicurezza (sopra) |
| `golang.org/x/crypto`, `x/image`, `x/oauth2`, `x/text`, `x/time` | minor indietro | — |
| `google.golang.org/api` | 0.264 → 0.301 | YouTube |
| `prometheus/client_golang` | 1.16 → 1.25 | — |
| `sirupsen/logrus` | 1.9.3 → 1.10.2 | — |
| `lib/pq` | 1.10.9 → 1.12.3 | — |
| `jmoiron/sqlx` | 1.3.5 → 1.4.0 | — |
| `microcosm-cc/bluemonday` | 1.0.24 → 1.0.27 | sanitizzatore HTML: preferibile tenerlo aggiornato |
| `mmcdole/gofeed` | 1.3 → 1.5 | RSS |
| `gobwas/ws` | 1.2.1 → 1.4.0 | gateway Discord: da provare con attenzione |
| `getsentry/sentry-go` | 0.22 → 0.50 | pre-1.0, possibili cambi di API; non lo usiamo (nessun DSN) |
| `volatiletech/sqlboiler/v4`, `null/v8`, `strmangle` | 4.14 → 4.19 ecc. | progetto passato a `aarondl/*`; i modelli vanno rigenerati insieme |
| `tidwall/buntdb`, `tkuchiki/go-timezone`, `stretchr/testify`, `gin-gonic/gin` | patch/minor | — |

## Major nuove (richiedono modifiche al codice)

Solo con un motivo concreto, e meglio upstream.

| Modulo | Attuale → nuova | Impatto |
|---|---|---|
| `mediocregopher/radix` | v3 → v4 | client Redis usato in ~70 file: lavoro grosso |
| `goji.io` | v2 → v3 | router del pannello in ~37 file; goji è fermo dal 2022 |
| `karlseguin/ccache` | v2 → v3 (generico) | 8 file |
| `didip/tollbooth` | v4 → v8 | rate limit di `premium/` |
| `shirou/gopsutil` | v3 (vecchio percorso) → v4 | `bot/memwatcher.go`, `yagstatus` |
| `vmihailenco/msgpack` | v4 → v5 | serializza dati dei CC temporizzati salvati: attenzione al formato |
| `russross/blackfriday` | v1 → v2 | v1 richiesto anche da `github_flavored_markdown` |
| `natefinch/lumberjack` | percorso non canonico → `gopkg.in/natefinch/lumberjack.v2` | solo cambio di import |

## Da lasciare invariati

| Modulo | Perché |
|---|---|
| `emperror.dev/errors` | ~90 file del core; fermo dal 2022 ma stabile. Unificare le librerie di errori creerebbe conflitti a ogni sync (decisione del 2026-10-10). |
| `friendsofgo/errors`, `volatiletech/strmangle` | richiesti dal codice generato da sqlboiler |
| `pkg/errors` | usato solo dalle librerie forkate in `lib/` |
| `karlseguin/rcache`, `patrickmn/go-cache`, `lunixbochs/vtclean`, `ei14/calc`, `shurcooL/github_flavored_markdown` | non più mantenuti ma piccoli, stabili e senza vulnerabilità note |
| `google/safebrowsing` | nessuna versione più recente pubblicata |
| `gorilla/schema`, `gorilla/websocket`, `NYTimes/gziphandler`, `bwmarrin/snowflake`, `hashicorp/go-cleanhttp` | aggiornati e in uso |
