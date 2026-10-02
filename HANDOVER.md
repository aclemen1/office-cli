# HANDOVER — dossier

2026-10-02. Checkout `/Users/aclemen1/code/aclemen1/dossier-cli`, sous **jj colocalisé**, **sans remote**. Trois commits : `tlmlluty` (prototype), `yrnwxkus` (seconde tranche), `mqyxpzql` (séances, alias, réveils, correctifs). Binaire installé : `~/go/bin/dossier`. Discussion avec Alain en français et au vouvoiement ; code, CLI et MCP en anglais (open source visé).

**Le concept** : Alain désigne lui-même ce qui mérite d'être suivi (push, pas pull). Un **signal** (étoile jaune Gmail, tâche Shift-T, drapeau Reminders, mémo vocal) ouvre ou rouvre un **dossier** ; chaque dossier a **sa session** Claude Code, lancée en arrière-plan dans un onglet herdr par `herdr-acp`, visible sur l'iPhone par Remote Control. Le dossier se clôt sur décision d'Alain, jamais à la sortie de la session. Ce projet remplace le brief et le cockpit de `~/self`.

## Reprendre

```sh
cd ~/code/aclemen1/dossier-cli
go test ./...                       # 7 paquets, faux serveur ACP et faux connecteur
go install ./cmd/dossier            # les sessions en cours prennent le nouveau binaire au prochain appel d'outil
dossier doctor --store ~/dossiers/pro --format text
dossier ls --store ~/dossiers/pro --format text
dossier tree --store ~/dossiers/pro RDIR --format text
dossier ingest --store ~/dossiers/perso --dry-run --format text   # lecture seule
```

Horloge : LaunchAgent `aero.clement.dossier-ingest` (chargé), `ingest` sur les deux stores toutes les 15 min. Journal `~/Library/Logs/dossier-ingest.log`. Modèle générique : `contrib/launchd/dossier-ingest.plist.example`.

## Architecture

| Paquet | Rôle |
|---|---|
| `internal/spec` | `ActionSpec` : source unique du CLI, de `schema` et du MCP ; analyse des arguments, enveloppe `{ok, result \| error}`, codes de sortie |
| `internal/store` | store = bundle OKF ; `config.toml`, résolution (`--store`, `DOSSIER_STORE`, répertoire courant, `default_store`), préfixe d'id, verrou, gabarits |
| `internal/dossier` | `dossier.md` (OKF, clés inconnues et corps préservés, bloc de liens géré), `log.md`, `.state.json` |
| `internal/app` | opérations : open/route/agenda, états et transitions, réveils, graphe, merge, grep, search, ingest, sessions ACP, vitrine de skills, garde-fou |
| `internal/acp` | client ACP minimal (JSON-RPC stdio) vers `herdr-acp` |
| `internal/connector` | protocole 1 des connecteurs : `describe`, `poll`, `transition` |
| `internal/actions` | déclaration des actions, `dossier mcp` (outils limités au dossier courant), skill embarqué |
| `connectors/` | `gmail.py`, `memos.py`, `reminders.py` (uv, autour de `gws` et `macos`) |

## Les stores réels

| | `~/dossiers/perso` (défaut) | `~/dossiers/pro` |
|---|---|---|
| Préfixe | `P` | `U` |
| Mémoire | `--add-dir ~/vaults/Alain Clément`, `qmd --index perso` | `--add-dir ~/vaults/UNIL`, `qmd --index pro` |
| Skills (vitrine `.claude/skills`) | qmd, gws, macos (généré) | qmd, gws, unil-docx, macos (généré) |
| Sources | gmail (perso), memos (client `dossier-perso`), reminders (`exclude_tag = "pro"`) | gmail (unisis, listes de séance), reminders (`require_tag = "pro"`) |
| Séances | — | U-RDIR, U-RTUT, U-PSEC, U-TBCI ; une liste Google Tasks du même nom chacune |

Charte des agents : `CLAUDE.md` à la racine de chaque store (chargé par toutes les sessions). Gabarits de prompt en français : `.dossier/prompts/{open,event,deadline}.md`. `00-Inbox` protégée (`agent.protect`) : règle `Edit` + hook Bash `dossier hook guard`.

## Écosystème

| Outil | Rôle et état |
|---|---|
| `herdr-acp` (`~/code/aclemen1/herdr-acp`) | serveur ACP ; mode `native` (`471273cd`) et `tabLabel` (`8346e07b`) consignés, **non poussés** ; utilisé par `node …/dist/index.js` (absent du `PATH`) |
| `gws` | Gmail et Tasks ; profils `~/.config/gws-perso`, `~/.config/gws-unisis` |
| `macos` | rappels (drapeaux, tags), mémos (client par connecteur, transcription `apple-speech`) |
| `~/self/bin/guard-vault-inbox.sh` | garde-fou du cockpit, corrigé le 02.10 pour `obsidian vault="…" move` |

## Décisions arrêtées

| Sujet | Décision |
|---|---|
| Signal Gmail | étoile jaune ; Shift-T facultatif (sa note = consigne ; une tâche sans étoile fait étoiler son message) ; premier passage : tous les fils étoilés |
| États ↔ source | `wait` violette, `resume` jaune, `close` étoiles retirées + `reviewed` + tâche cochée, `reopen` jaune + tâche décochée. Reminders : le drapeau joue l'étoile ; clôture = compléter + retirer le drapeau |
| Sphères | un store par sphère ; un id d'un autre préfixe est refusé ; mémos lus par le store perso seul ; rappel `#pro` → pro |
| Graphe | `includes` et `depends_on` seulement (`related` écarté) ; une séance inclut ses points ; un point clos reste à l'ordre du jour jusqu'à la séance |
| Attente | `wait --until`, **7 jours par défaut** (`lifecycle.default_wait`) ; à l'échéance, réveil et prompt de relance |
| Réveil | réponse sur un fil suivi, échéance, mémo adressé (« U-2 : … »), `notify` d'une séance |
| Onglets | fermés en `waiting` et `done` (`close_tab_on`) ; libellé « U-RDIR · 30 caractères » |
| Transcription | versionnée avec le store ; pas de résumé à part (la transcription garde tout, `grep` y cherche) |
| Prompts | horodatés en tête, jour de la semaine compris (`[prompt] locale = "fr"`) |

## Pièges connus

- **Le serveur MCP d'une session est un processus long.** Chaque appel d'outil passe par le binaire installé (`runInstalled`), mais la **liste des outils** reste celle du démarrage : un outil ou un paramètre nouveau exige `dossier restart <id>`. En test, `runAction = runInProcess`, sinon le binaire de test se relance lui-même à l'infini.
- **`gws` n'imprime rien pour une réponse vide.** Tout masque `fields` garde un scalaire toujours présent (`historyId`, `resultSizeEstimate`, `etag`).
- **Une réponse n'est pas un message de soi.** Le connecteur écarte `SENT`, `DRAFT`, `SCHEDULED`, l'expéditeur titulaire du compte et tout message daté après le passage.
- **`--add-dir` est variadique** : il avale un argument libre qui le suit (sans effet pour `dossier`, qui envoie le prompt par ACP).
- **Les `CLAUDE.md` parents se cumulent** jusqu'à la racine : un store ne vit jamais sous `~/self`. Les skills, elles, ne se lisent que dans le `.claude/` le plus proche.
- **Règles de permission sur un chemin : seul `Edit(...)` compte** ; `Write(path)` est ignoré avec un avertissement.
- **Le garde-fou du cockpit refuse toute commande Bash qui cite `00-Inbox` avec un verbe mutant**, même dans un heredoc de test : écrire ces fichiers avec l'outil d'écriture, et nommer le répertoire protégé autrement dans les tests (`Private`).
- **Une tâche Shift-T ne se voit pas dans la liste Gmail** : d'où l'étoile comme signal.
- **Une liste de tâches nouvellement déclarée est lue en entier** au premier passage (curseur `lists`).

## Ouvert

| Point | Note |
|---|---|
| Étoile d'un brouillon à l'envoi | `track` + `wait` posent la violette sur le brouillon ; non vérifié que Gmail la garde à l'envoi |
| Connecteurs memos et reminders en réel depuis le LaunchAgent | TCC de `macos` dans le contexte launchd non vérifié |
| Remote et push | `origin` = github.com/aclemen1/dossier-cli (public, MIT) ; `herdr-acp` a deux commits non poussés |
| Open source | README, `skill install` pour d'autres harnais que Claude |
| Tests du code Python | aucun test automatique des connecteurs |
| Mémoire OKF | serveur distinct prévu ; migration du cockpit vers elle |
| Mémoire d'une instance Claude au repos | non mesurée ; swap à 98 % observé le 01.10 |
| Rubriques « Prochaine séance » des séances | reprises du brief avec des dates passées |
