# Tampon mélangé et écritures groupées, sur une seule instance

Deux tables sans clé commune ne suffisent pas. L'ordre d'écriture (ordre physique dans PostgreSQL, identifiants séquentiels, journaux de transactions) permettrait d'apparier une participation et une réponse, surtout en le recoupant avec le journal du fournisseur de codes.

Nous gardons donc chaque envoi (participation et réponse) dans un tampon en mémoire de l'API. Le tampon est écrit en une seule transaction dès qu'il compte 20 envois, ou au plus tard toutes les 30 minutes. Participations et réponses y sont mélangées séparément, dans un ordre aléatoire. Les participations n'ont pas d'horodatage, les réponses ne gardent que le jour, et tous les identifiants sont aléatoires.

## Consequences

- L'API tourne sur **une seule instance**. La détection des doublons consulte la base et le tampon. Passer à plusieurs instances demanderait de revoir ce mécanisme.
- Si le serveur plante, jusqu'à 30 minutes d'envois sont perdus. Comme la participation est perdue avec la réponse, la personne peut répondre à nouveau. À l'arrêt normal, le tampon est écrit.
- Aux heures creuses, un lot peut compter moins de 20 envois. Ce risque est accepté : le seuil de 1 000 réponses avant publication limite ce qu'on peut en tirer.
