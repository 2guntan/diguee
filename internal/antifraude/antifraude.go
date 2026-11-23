// Package antifraude porte les limites et la surveillance qui précèdent la
// vérification d'un numéro (spec §7, ADR 0007) :
//
//   - Fenetre : compteur en mémoire par clé dans une fenêtre glissante, avec
//     une mémoire bornée (sert aussi à l'espace équipe) ;
//   - Anonymiseur : remplace une adresse IP par un HMAC dont le sel, tiré au
//     hasard et gardé en mémoire, change chaque jour ;
//   - LimiteEmpreinte : au plus DIGUEE_VERIF_PAR_EMPREINTE_24H vérifications
//     ou codes par empreinte en 24 heures (vérification inversée, codes
//     WhatsApp et SMS) ;
//   - Ralentisseur : au-delà de DIGUEE_IP_PAR_HEURE demandes par heure depuis
//     une même adresse, un délai croissant et borné, et une alerte, sans
//     jamais bloquer (CGNAT des opérateurs) ;
//   - Surveillance : métriques agrégées par préfixe et par région, avec une
//     alerte en cas de pic anormal.
//
// Rien de ce paquet n'est écrit en base. Ni adresse IP, ni numéro, ni
// empreinte, ni HMAC d'adresse n'est journalisé.
package antifraude
