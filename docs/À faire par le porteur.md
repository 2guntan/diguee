# À faire par le porteur

Ce document liste tout ce que **seul le porteur** peut fournir : comptes, accès, contenus et choix. En attendant, le code tourne avec des **fausses implémentations** et des **valeurs provisoires**. Quand l'élément est disponible, il suffit de changer la configuration, **sans modifier le code**.

**Règle de sécurité** : avec `DIGUEE_ENV=production`, le serveur **refuse de démarrer** s'il reste une fausse implémentation ou une valeur « À COMPLÉTER ». Au démarrage, il affiche la liste exacte de ce qui manque.

Chaque section indique : ce qui tourne en attendant, ce qu'il faut faire, et le réglage à changer. Les noms exacts des variables sont définis par le code. Ce document est mis à jour au fil des tickets (voir « État d'avancement » en bas).

---

## 1. Comptes et services externes

### 1.1 OVHcloud : KMS pour l'empreinte (tickets #5, #7, #15)

- **En attendant** : `EMPREINTEUR=fake`, un HMAC calculé avec une clé de développement. Ce mode est interdit en production.
- **À faire** :
  1. Créer le compte OVHcloud **réservé à Diguee**, avec la double authentification.
  2. Commander un KMS en région France, puis créer une clé RSA non exportable, utilisable pour signer avec l'algorithme **RS256**.
  3. Créer un accès (certificat ou jeton) qui n'a que la permission `kms:serviceKey/sign` sur cette clé.
- **Réglage** : `EMPREINTEUR=ovh`, avec l'identifiant de la clé, le point d'accès régional et l'accès créé.
- **Vérification** : au démarrage, le serveur contrôle que la signature est déterministe. S'il refuse de démarrer, il faut basculer sur Google Cloud KMS (`EMPREINTEUR=gcp`, ADR 0006).

### 1.2 OVHcloud : VPS et DNS (ticket #7)

- **En attendant** : le développement se fait en local. Les scripts de déploiement sont écrits, mais n'ont pas tourné sur un vrai serveur.
- **À faire** :
  1. Commander un VPS-1 en région France, réservé à Diguee.
  2. Faire pointer `consultation.diguee.sn` vers son adresse IP. La zone DNS est chez N0C, sur PlanetHoster.
  3. Exécuter la procédure de déploiement documentée par le ticket #7.

### 1.3 OVHcloud : stockage d'objets (tickets #13, #15)

- **En attendant** : `STOCKAGE=fake`, un dossier local.
- **À faire** : créer un bucket S3 en France et une clé d'accès limitée à ce bucket.
- **Réglage** : `STOCKAGE=ovh`, avec le point d'accès, le bucket et la clé.

### 1.4 Clé de chiffrement des sauvegardes (ticket #15)

- **En attendant** : une paire de clés `age` de test, générée pour les tests.
- **À faire** : générer **vous-même** une paire de clés `age` sur votre ordinateur (`age-keygen`). Gardez la clé privée **hors ligne**, dans un gestionnaire de mots de passe et sur un support de secours, et donnez seulement la clé publique.
- **Réglage** : la clé publique des sauvegardes.

### 1.5 Meta : WhatsApp Business (tickets #6, #9)

- **En attendant** : `WHATSAPP=fake`. Une page de développement simule l'envoi du message par le citoyen (vérification inversée), et les codes de secours sont affichés dans la console. Ce mode est interdit en production.
- **À faire** :
  1. Créer le portefeuille business Meta et l'application WhatsApp, avec la vérification de l'entreprise.
  2. Obtenir le **numéro WhatsApp de Diguee**.
  3. Faire approuver un modèle de message d'**authentification** pour le code de secours.
  4. Déclarer l'adresse du webhook (`https://consultation.diguee.sn/webhook/whatsapp`) avec un jeton de vérification.
- **Réglage** : `WHATSAPP=meta`, avec l'identifiant du numéro, le jeton d'accès, le secret de l'application (qui sert à vérifier les signatures), le jeton de vérification du webhook et le nom du modèle.
- **À vérifier** : le tarif réel pour le Sénégal dans votre gestionnaire WhatsApp.

### 1.6 Fournisseur SMS (ticket #10)

- **En attendant** : `SMS=fake`, avec les codes affichés dans la console.
- **À faire** : choisir un fournisseur qui a un **accord direct avec les opérateurs sénégalais** (ADR 0004), ouvrir le compte et régler un **plafond de dépenses journalier** chez lui.
- **Réglage** : `SMS=<nom du fournisseur>`, avec ses accès. Un petit adaptateur sera à écrire pour ce fournisseur : il implémente l'interface `Envoyeur`.

## 2. Contenus et valeurs à fournir

Ces valeurs sont « À COMPLÉTER » dans la configuration. Elles s'affichent comme telles sur les pages de développement.

| Élément | Où il apparaît | Ticket |
|---|---|---|
| Nom et adresse de la structure (responsable du traitement) | Politique de confidentialité, premier message WhatsApp | #1, #6 |
| Numéro CDP | Politique de confidentialité, pied de page | #1 |
| Adresse de contact (droits d'accès et d'opposition) | Politique de confidentialité | #1 |
| Date d'ouverture (la clôture tombe 6 mois plus tard) | Accueil, méthodologie, clôture | #1, #16 |
| Échelles de réponse de Q1 et Q2 (des propositions figurent dans la spec §3) | Questionnaire | #2 |
| Choix de réponse de Q5 (« qui devrait évaluer ») | Questionnaire | #2 |
| Liste définitive des préfixes mobiles (ARTP) | Vérification | #6 |
| Traductions en wolof et en pulaar | Catalogues `wo` et `ff` | #1 |
| Composition de la population (ANSD) par région, âge et sexe | Page des résultats | #11 |
| Premiers élus et engagements de l'observatoire | Observatoire | #13 |

## 3. Actions humaines prévues

- Recopier `site/index.html` (FAQ corrigée) dans `public_html/` chez PlanetHoster.
- Créer les comptes de l'équipe (deux personnes) dans l'espace équipe, avec un TOTP chacun.
- Publier chaque semaine sur X l'empreinte du journal préparée par la plateforme.
- Ticket #18 : audit en préproduction et test d'intrusion avant le lancement.
- Fusionner la branche de travail dans `main` quand vous le jugerez bon.

---

## État d'avancement du développement

Mis à jour à la fin de chaque ticket : ce qui est fait, et les éventuelles valeurs provisoires ajoutées.

| Ticket | État | Notes |
|---|---|---|
