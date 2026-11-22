# WhatsApp en canal principal, SMS en secours

Les codes de vérification partent d'abord par WhatsApp (API Cloud de Meta, modèle de message d'authentification). WhatsApp est très utilisé au Sénégal, y compris par les personnes âgées. Il écarte presque entièrement la fraude au SMS (menace nº 2 du handoff) et coûte moins cher par code. Le SMS reste un secours, proposé par un lien discret « Je n'ai pas WhatsApp ». Il passe par un seul fournisseur qui a un accord direct avec les opérateurs sénégalais, avec un plafond de dépenses strict et des limites plus sévères que sur WhatsApp.

## Consequences

- Meta voit le numéro et l'heure de la demande de code. C'est un transfert hors du Sénégal, à déclarer à la CDP (§4.3).
- La vérification d'entreprise par Meta et le contrat SMS supposent une structure juridique propre à Diguee, distincte de l'autre entreprise du porteur.
