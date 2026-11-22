# Empreinte calculée dans un KMS, détruite à la clôture

Les numéros mobiles sénégalais tiennent dans un espace d'environ 50 millions de valeurs. Quiconque possède à la fois la base et la clé HMAC peut donc retrouver tous les numéros qui ont participé (mais pas leurs réponses, voir ADR 0001). Nous avons décidé que la clé HMAC est gardée dans le service de gestion de clés (KMS) de l'hébergeur, sans possibilité de l'exporter. L'API demande au KMS de calculer l'empreinte, sans jamais voir la clé. À la clôture, six mois après l'ouverture, nous détruisons la clé et la table des participations, puis nous publions le procès-verbal de cette destruction dans le journal public.

## Consequences

- Le risque restant est accepté : pendant que la consultation est ouverte, quelqu'un qui obtiendrait à la fois la base et l'accès au KMS pourrait savoir qui a participé.
- La consultation a une date de clôture fixe. Après la clôture, il n'est plus possible de répondre ni de repérer un doublon.
- L'hébergeur choisi doit proposer un KMS avec des clés non exportables.
- Le site ne parle plus de numéro « chiffré », mais d'« empreinte ».
