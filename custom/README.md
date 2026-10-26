# Plugin custom (fork Haxurus)

Qui vivono le funzioni aggiunte dal fork. Stanno fuori dai package upstream così ogni aggiornamento da `botlabs-gg/yagpdb` si unisce senza conflitti ([docs/UPSTREAM.md](../docs/UPSTREAM.md)).

## Plugin presenti

| Plugin | Cosa fa | Configurazione |
|---|---|---|
| [`logretention`](logretention/logretention.go) | Cancella i message log più vecchi di N giorni, al posto della cancellazione fissa a 30 giorni dell'upstream. Il valore si cambia senza riavvio. | `yagpdb.custom.message_log_retention_days` (default 30, `0` = conserva tutto) da `/admin/config`, oppure `YAGPDB_CUSTOM_MESSAGE_LOG_RETENTION_DAYS` in `steward.env`. Richiede `YAGPDB_ENABLE_MESSAGE_LOG_PURGE=false` (già impostato nel Compose di produzione). |
| [`tempvoice`](tempvoice/tempvoice.go) | Canali vocali temporanei: chi entra in un canale "crea canale" riceve un canale vocale suo (eliminato quando si svuota), gestito da un pannello unico con pulsanti (rinomina, limite, blocca, nascondi, invita, espelli, trasferisci, rivendica, elimina). | Pannello web **Canali temporanei** (sezione Tools): canali hub, categoria, canale del pannello, nome, limite iniziale. Il bot richiede *Gestisci canali*, *Gestisci permessi*, *Sposta membri*. |
| [`levels`](levels/levels.go) | Esperienza e livelli da messaggi, reazioni (data e ricevuta) e minuti in vocale, con controlli anti-abuso: attese, lunghezza minima, una reazione premiata per messaggio, limiti giornalieri per fonte, niente XP nel canale AFK / da soli / con microfono o audio disattivati, anti-AFK basato sull ultima attività, canali e ruoli esclusi. Curva dei livelli e ruoli premio configurabili, annunci, classifica pubblica. | Pannello web **Livelli** (sezione Fun). Comandi `/livello`, `/classifica`, `/xp aggiungi|togli|imposta|azzera` (Gestisci server). |

## Aggiungere un plugin

1. Creare `custom/<nome>/` con un `RegisterPlugin()` che chiama solo `common.RegisterPlugin(...)` e, se servono tabelle, `common.RegisterDBSchemas("<nome>", DBSchemas...)`. La registrazione avviene in `init()` (da `cmd/yagpdb/custom_plugins.go`), prima che configurazione, Redis e PostgreSQL siano pronti: niente `common.InitSchemas`, niente query.
2. Aggiungere la chiamata in [`custom.go`](custom.go).
3. Implementare le interfacce che servono, come fanno i plugin upstream (esempi compatti: `reminders/` per bot e comandi, `reputation/` per il pannello):
   - `bot.BotInitHandler` per gli handler di eventi (`eventsystem.AddHandlerAsyncLastLegacy`);
   - `commands.CommandProvider` per i comandi (`commands.AddRootCommands`, container + `commands.RegisterSlashCommandsContainer`);
   - `web.Plugin` per le pagine del pannello (`web.CPMux`, template con `//go:embed` e `web.AddHTMLTemplate`);
   - `backgroundworkers.BackgroundWorkerPlugin` per i lavori periodici.
4. Nomi univoci per non scontrarsi con l'upstream: `SysName` con prefisso `custom_`, opzioni `yagpdb.custom.*`, tabelle con prefisso `custom_`, chiavi Redis con prefisso `custom:`.
5. Test accanto al codice. Se servono PostgreSQL o Redis, usare lo stesso `TestMain` di `logretention_test.go`: in CI fallisce se i servizi mancano invece di saltare i test.

Il calcolo dei package da testare in CI include automaticamente i plugin custom toccati e tutto ciò che ne dipende.
