# Hébergement de la plateforme entièrement chez OVHcloud, en France

La consultation (API Go et PostgreSQL) tourne sur un VPS OVHcloud en région France, réservé à Diguee. Le KMS d'OVHcloud calcule l'empreinte (ADR 0002), et le stockage d'objets d'OVHcloud reçoit les sauvegardes chiffrées. Nous avons choisi un seul fournisseur et une seule facture, pour environ 5 à 9 € HT par mois, plutôt que de séparer le serveur et les clés chez deux fournisseurs. Le fournisseur est européen et n'est pas exposé au CLOUD Act américain.

## Considered Options

- **Hetzner pour le serveur et OVHcloud pour les clés et les sauvegardes** : meilleure séparation des comptes, mais pas moins cher et plus lourd à gérer.
- **Contabo** : la puissance la moins chère, mais une fiabilité et des outils de sécurité (pare-feu, instantanés) plus faibles. Ce n'est pas le bon choix pour un service dont la crédibilité est le capital.
- **AWS ou Google Cloud (région Paris)** : HMAC natif dans leur KMS, mais ce sont des sociétés américaines. Google Cloud KMS reste la solution de repli si le KMS d'OVHcloud ne convient pas.
- **Hébergement au Sénégal** : pas de KMS géré disponible, donc l'ADR 0002 ne pourrait pas être tenu.

## Consequences

- Le KMS d'OVHcloud ne propose pas de HMAC. L'empreinte est donc `SHA-256(signature RSA PKCS#1 v1.5 du numéro)`, avec une clé qu'on ne peut pas extraire. Avant de s'engager, il faut vérifier que la signature est bien déterministe. La construction devra aussi être relue lors du test d'intrusion.
- Le compte OVHcloud est réservé à Diguee, protégé par une double authentification, et n'est jamais partagé avec l'autre entreprise du porteur.
- PostgreSQL est administré par nous : disque chiffré, mises à jour automatiques, port fermé à l'extérieur, test de restauration chaque mois.
