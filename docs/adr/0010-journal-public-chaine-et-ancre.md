# Journal public chaîné et ancré hors de nos serveurs

Un journal hébergé seulement chez nous ne prouve rien, puisque nous pourrions le réécrire. Chaque entrée du journal public contient donc l'empreinte SHA-256 de l'entrée précédente. Le journal enregistre les publications quotidiennes, les changements de statut de l'observatoire, les mises en production, les paramètres de la méthodologie et le procès-verbal de destruction à la clôture.

Chaque jour, la dernière empreinte est horodatée avec OpenTimestamps et déposée sur archive.org. Chaque semaine, le porteur la publie lui-même sur X. Le dépôt du code est public avant le lancement, et l'exécutable est reproductible : le pied de page affiche la version en production, que n'importe qui peut reconstruire.

## Consequences

- Les réponses individuelles ne sont jamais publiées, même anonymes : avec quatre critères de profil, une réponse isolée resterait identifiante. Seuls les agrégats masqués sont publiés.
- Un script de vérification de la chaîne est publié avec le code.
- La plateforme prépare la publication hebdomadaire sur X, mais ne la poste jamais elle-même.
