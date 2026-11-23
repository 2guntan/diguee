# WhatsApp : vérification inversée, sans bot de consultation

Cet ADR précise l'ADR 0004. WhatsApp sert seulement à vérifier le numéro, jamais à répondre au questionnaire. Un bot obligerait Meta à voir ensemble le numéro et les réponses, ce qui recréerait chez Meta le lien entre identité et réponse que les ADR 0001, 0003 et 0007 suppriment.

En parcours principal, la vérification est inversée. Le site affiche un jeton à usage unique, et le citoyen l'envoie à Diguee par WhatsApp, via un message prérempli sur mobile ou un QR code sur ordinateur. Le numéro est celui que Meta nous transmet comme expéditeur : il n'est jamais saisi sur le site. En secours, Diguee envoie un code par WhatsApp. En dernier recours, le code part par SMS (ADR 0004).

## Consequences

- La page de vérification a besoin d'un peu de JavaScript pour détecter la réception du message. Sans JavaScript, un bouton « J'ai envoyé le message » fait le même travail.
- Un jeton est valable 10 minutes. On autorise 3 vérifications par numéro et par 24 h.
- Diguee doit recevoir les messages entrants par webhook sur l'API Cloud de Meta. Ce webhook fait partie de la surface d'attaque, et il faut vérifier sa signature.
- Le coût des messages entrants est à vérifier dans la grille de Meta en vigueur.
