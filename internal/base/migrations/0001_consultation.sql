-- Schéma de la consultation (spec §6.1).

-- Aucun horodatage, aucune séquence. Détruite à la Clôture.
CREATE TABLE participation (
  empreinte bytea PRIMARY KEY CHECK (octet_length(empreinte) = 32)
);

-- Aucun identifiant de personne. Conservée après la Clôture.
CREATE TABLE reponse (
  id                    uuid PRIMARY KEY,  -- aléatoire (v4), jamais séquentiel
  jour                  date NOT NULL,     -- jour d'écriture du lot, pas l'heure
  version_questionnaire smallint NOT NULL,
  q1 smallint,
  q2 smallint,
  q3 smallint NOT NULL,
  q4 smallint,
  q5 smallint,
  q6 smallint,
  q7 smallint[],                           -- choix multiples
  themes smallint[] CHECK (themes IS NULL OR cardinality(themes) IN (0, 3)),
  region smallint,
  tranche_age smallint,
  sexe smallint,
  inscrit smallint
);
