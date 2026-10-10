# Sincronizzazione con l'upstream

Steward (`haxurus/steward`) è il fork Haxurus di YAGPDB e segue `botlabs-gg/yagpdb` con due branch, come Muse:

| Branch | Contenuto |
|---|---|
| `master` | specchio esatto di `botlabs-gg/yagpdb:master`. Avanza solo in fast-forward: **mai** commit propri qui. |
| `main` | produzione Haxurus: `master` + CI, deploy, plugin custom. Default branch, da qui parte il deploy. |

L'obiettivo è che ogni aggiornamento upstream si unisca senza conflitti. Per questo il codice del fork vive in file e cartelle che l'upstream non ha.

## Dove sta il codice del fork

| Percorso | Contenuto |
|---|---|
| `.github/workflows/`, `.github/scripts/`, `.github/dependabot.yml` | CI, deploy, sync (l'upstream usa CircleCI e non ha workflow) |
| `deploy/`, `ops/`, `security/` | immagine, Compose, script VPS ([DEPLOYMENT.md](DEPLOYMENT.md)) |
| `custom/`, `cmd/yagpdb/custom_plugins.go` | plugin custom ([custom/README.md](../custom/README.md)) |
| `docs/DEPLOYMENT.md`, `docs/UPSTREAM.md`, `docs/DEPENDENCIES.md` | documentazione del fork |
| `*_db_test.go` e altri file di test nuovi | test aggiunti dal fork |

**File upstream modificati dal fork: nessuno.** Se diventa indispensabile toccarne uno, aggiungerlo alla tabella qui sotto con il motivo: sono i punti dove possono nascere conflitti.

| File upstream | Modifica | Motivo |
|---|---|---|
| — | — | — |

Regole:

- una funzione nuova va in un plugin in `custom/`, non dentro un plugin upstream;
- per cambiare il comportamento di un plugin upstream, preferire la configurazione (`steward.env`, `/admin/config`) o un plugin custom che lo affianca;
- non aggiornare dipendenze in `go.mod` nel fork: arrivano dall'upstream (vedi [DEPENDENCIES.md](DEPENDENCIES.md)); fa eccezione una correzione di sicurezza urgente, da proporre anche upstream.

## Sincronizzazione automatica

Il workflow **Sync upstream** (ogni giorno alle 05:41 UTC, oppure a mano da Actions):

1. porta `master` a `upstream/master` (solo fast-forward) e copia i tag upstream sul fork (servono a `git describe` per la versione dell'immagine);
2. se `main` non contiene ancora quei commit, crea `sync/upstream` da `main`, fa il merge di `upstream/master` e apre (o aggiorna) la PR **Sync upstream vX.Y.Z** verso `main`;
3. avvia la CI su `sync/upstream` con base `main`: vengono testati i package che l'upstream ha cambiato e quelli che ne dipendono; se cambia `go.mod` gira tutta la suite più `govulncheck`.

Il merge della PR su `main` avvia il deploy come qualsiasi altra modifica.

Il branch `sync/upstream` viene ricreato a ogni esecuzione (force push): non committare lì a mano.

### Prima del merge di una PR di sync

- CI verde.
- `breaking_changes.md`: se è cambiato, leggere le novità. Variabili rinominate o default cambiati vanno riportati in `/srv/docker/steward/steward.env` e in `deploy/steward.env.example` **prima** del deploy.
- `yagpdb_docker/Dockerfile` e `yagpdb_docker/app.example.env`: se cambia la versione di Go, di Alpine o un pacchetto di sistema, allineare `deploy/Dockerfile`. Una nuova variabile d'ambiente obbligatoria va in `steward.env`.
- `cmd/yagpdb/main.go`: plugin nuovi o rimossi. I plugin custom non sono in questo file, quindi qui non ci sono conflitti.
- Plugin con nuove chiavi API: aggiungere il segreto in `secrets/app/` solo se si vuole usarli.
- Nome Steward: testi nuovi con "YAGPDB" vengono rinominati da `deploy/rebrand.sed` senza interventi; il job `build` compila la versione rinominata, quindi una regola che non regge più fa fallire la CI della PR di sync. Frasi riscritte per intero (crediti, tagline) vanno aggiornate in `rebrand.sed` se l'upstream le cambia: la CI non fallisce, ma il testo tornerebbe quello generico.

### Conflitti

Il run fallisce, il riepilogo del job elenca i file in conflitto e GitHub manda la notifica del workflow fallito. Si risolve a mano:

```bash
git fetch origin
git fetch upstream            # remote upstream = https://github.com/botlabs-gg/yagpdb.git
git checkout -b sync/upstream-manual origin/main
git merge upstream/master     # risolvere i conflitti, poi:
git commit
git push -u origin sync/upstream-manual
gh pr create --base main --head sync/upstream-manual --title "Sync upstream (manual)"
```

Se l'upstream modifica file in `.github/workflows/`, il push automatico di `master` fallisce perché il `GITHUB_TOKEN` non può scrivere workflow: in quel caso aggiornare `master` a mano (`git push origin upstream/master:master`).

## Contribuire all'upstream

Solo per bug e problemi di sicurezza nel codice esistente:

```bash
git fetch upstream
git checkout -b fix/<descrizione> upstream/master
# modifica + test
git push -u origin fix/<descrizione>
gh pr create --repo botlabs-gg/yagpdb --base dev --head haxurus:fix/<descrizione>
```

Come da `CONTRIBUTING.md`, le PR upstream vanno verso `dev`. L'upstream non ha una policy di sicurezza (`SECURITY.md`): una vulnerabilità va segnalata in privato ai maintainer, tramite il [server di supporto](https://discord.gg/4udtcA5) indicato nel README, prima di aprire una PR pubblica. Il fix entra in `main` con la sincronizzazione successiva; se è urgente si può fare cherry-pick su un branch da `main` nel frattempo.
