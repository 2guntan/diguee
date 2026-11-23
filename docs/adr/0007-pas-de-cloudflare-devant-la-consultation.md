# Pas de Cloudflare devant la consultation, et un captcha ALTCHA auto-hébergé

Le handoff (§8) prévoyait Cloudflare devant le site. Or un proxy qui filtre le trafic doit le déchiffrer. Cloudflare verrait donc ensemble le numéro, le code et les réponses, avec l'adresse IP : c'est exactement le lien identité-réponse que les ADR 0001 à 0003 suppriment dans la base. Le chiffrement TLS s'arrête donc sur notre VPS. La saturation est absorbée par l'anti-DDoS réseau qu'OVHcloud fournit avec ses VPS, par les limites de l'API et par des pages légères. Le captcha est ALTCHA (preuve de travail, libre et hébergé chez nous) à la place de Cloudflare Turnstile, pour qu'aucun service tiers ne voie l'IP des participants.

## Consequences

- La consultation est servie depuis un sous-domaine (par exemple `consultation.diguee.sn`) qui pointe vers le VPS. La vitrine reste chez PlanetHoster.
- Une attaque applicative sophistiquée reste un risque accepté. En cas de besoin, Cloudflare peut être placé devant les pages publiques seulement (résultats, observatoire), jamais devant le parcours de réponse.
