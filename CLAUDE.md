# Diguee

## Agent skills

### Issue tracker

Les tickets vivent dans les GitHub Issues de `2guntan/diguee`. See `docs/agents/issue-tracker.md`.

### Triage labels

Étiquettes par défaut : needs-triage, needs-info, ready-for-agent, ready-for-human, wontfix. See `docs/agents/triage-labels.md`.

### Domain docs

Single-context : `CONTEXT.md` et `docs/adr/` à la racine. See `docs/agents/domain.md`.

## Conventions de développement

- Langue : documentation, messages de commit et textes visibles en français. Identifiants du code en français ou en anglais, au choix, mais cohérents (préférer les termes de `CONTEXT.md`).
- Référence : `docs/Spec plateforme Diguee v1.md`, les ADR de `docs/adr/` et les issues GitHub #1 à #18. Les ADR priment.
- Go 1.24, bibliothèque standard en priorité. Dépendances autorisées : `pgx`, `ovh/okms-sdk-go`, `golang.org/x/crypto` ; toute autre est à justifier dans le commit.
- **Fausses implémentations** : chaque service externe (KMS, WhatsApp, SMS, stockage d'objets, ancrage) passe par une interface, avec une implémentation `fake` choisie par la configuration. Avec `DIGUEE_ENV=production`, le serveur refuse de démarrer si une implémentation `fake` est active ou si une valeur de configuration vaut « À COMPLÉTER ».
- Toute nouvelle fausse implémentation ou valeur provisoire doit être ajoutée à `docs/À faire par le porteur.md`, avec le réglage qui la remplace.
- Ne jamais écrire de numéro de téléphone, d'IP ni de corps de requête dans les journaux, même en développement.
- Tests : `go test ./...` doit passer. Les tests qui demandent PostgreSQL utilisent la base locale (PostgreSQL 16).
