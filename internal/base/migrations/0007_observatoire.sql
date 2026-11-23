-- Observatoire (spec §6.4 et §9, ADR 0008, ticket #13) : élus, engagements,
-- sources et statuts, en ajout seul.
--
-- Modèle : aucune ligne n'est jamais modifiée ni supprimée. Chaque étape du
-- circuit est une ligne ajoutée dans sa propre table, qui référence l'étape
-- précédente ; l'état courant se lit en cherchant la dernière étape
-- atteinte :
--
--   engagement ─► engagement_validation (relecteur ≠ rédacteur : l'engagement est publié)
--
--   statut (brouillon, avec statut_preuve)
--     ─► statut_proposition (par son rédacteur)
--          ─► statut_validation (relecteur ≠ rédacteur)  ─► statut_publication (+ journal public)
--          └► statut_rejet      (relecteur ≠ rédacteur, fin du circuit)
--
-- Les clés étrangères imposent l'ordre des étapes, les clés primaires qu'une
-- étape n'a lieu qu'une fois. La règle des quatre yeux est une contrainte
-- CHECK (valide_par <> redige_par) : la colonne redige_par est recopiée dans
-- statut_validation, et une clé étrangère composée (statut_id, redige_par)
-- vers statut garantit que la copie est exacte. Le ticket #14 (préavis et
-- droit de réponse) ajoutera ses étapes de la même façon (statut_preavis,
-- droit_reponse…), et remplacera la contrainte statut_publiable_sans_preavis.
--
-- Comme ailleurs (T1), rien n'est plus fin que le jour : les colonnes *_le
-- sont des dates, l'ordre d'un même jour est celui des identifiants. Les
-- identifiants sont attribués par l'application (max + 1, sous un verrou
-- consultatif), sans séquence. Les dates sont celles d'actes publics de
-- l'équipe, jamais d'un participant.

CREATE TABLE elu (
  id               bigint PRIMARY KEY CHECK (id > 0),
  nom              text NOT NULL CHECK (char_length(nom) BETWEEN 1 AND 200),
  fonction         text NOT NULL CHECK (char_length(fonction) BETWEEN 1 AND 200),
  mandat_debut     date NOT NULL,
  mandat_fin       date NOT NULL CHECK (mandat_fin > mandat_debut),
  contact_officiel text NOT NULL CHECK (char_length(contact_officiel) <= 500),  -- canal du préavis (#14), jamais publié
  saisi_par        uuid NOT NULL REFERENCES membre (id),
  saisi_le         date NOT NULL
);

-- Une source : le lien d'origine et la copie archivée par Diguee, stockée
-- dans le stockage d'objets sous son SHA-256 (la clé de l'objet se déduit
-- de archive_sha256 : pas de colonne de chemin).
CREATE TABLE source (
  id             bigint PRIMARY KEY CHECK (id > 0),
  url            text NOT NULL CHECK (url ~ '^https?://[^[:space:]]+$' AND length(url) <= 2048),
  titre          text NOT NULL CHECK (char_length(titre) BETWEEN 1 AND 300),
  date_source    date NOT NULL,
  archive_sha256 bytea NOT NULL CHECK (octet_length(archive_sha256) = 32),
  archive_type   text NOT NULL CHECK (archive_type IN ('application/pdf', 'image/png', 'image/jpeg')),
  archive_taille integer NOT NULL CHECK (archive_taille > 0),
  ajoute_par     uuid NOT NULL REFERENCES membre (id),
  ajoute_le      date NOT NULL
);

CREATE INDEX source_archive ON source (archive_sha256);

CREATE TABLE engagement (
  id               bigint PRIMARY KEY CHECK (id > 0),
  elu_id           bigint NOT NULL REFERENCES elu (id),
  citation         text NOT NULL CHECK (char_length(citation) BETWEEN 1 AND 5000),
  contexte         text NOT NULL CHECK (char_length(contexte) <= 5000),
  date_declaration date NOT NULL,
  echeance         date NOT NULL CHECK (echeance >= date_declaration),
  cree_par         uuid NOT NULL REFERENCES membre (id),
  cree_le          date NOT NULL,
  UNIQUE (id, cree_par)
);

CREATE INDEX engagement_elu ON engagement (elu_id);

CREATE TABLE engagement_source (
  engagement_id bigint NOT NULL REFERENCES engagement (id),
  source_id     bigint NOT NULL REFERENCES source (id),
  PRIMARY KEY (engagement_id, source_id)
);

-- Validation d'un engagement par un relecteur distinct de son rédacteur. Un
-- engagement validé est publié (publie_le de la spec = valide_le) ; il
-- n'apparaît toutefois sur les pages publiques qu'avec un premier statut
-- publié, inscrit au journal public.
CREATE TABLE engagement_validation (
  engagement_id bigint PRIMARY KEY,
  cree_par      uuid NOT NULL,
  valide_par    uuid NOT NULL REFERENCES membre (id),
  valide_le     date NOT NULL,
  FOREIGN KEY (engagement_id, cree_par) REFERENCES engagement (id, cree_par),
  CONSTRAINT engagement_quatre_yeux CHECK (valide_par <> cree_par)
);

CREATE TABLE statut (
  id            bigint PRIMARY KEY CHECK (id > 0),
  engagement_id bigint NOT NULL REFERENCES engagement_validation (engagement_id),  -- engagement validé
  valeur        text NOT NULL CHECK (valeur IN ('tenu', 'partiel', 'non_tenu', 'en_cours')),
  explication   text NOT NULL CHECK (char_length(explication) BETWEEN 1 AND 5000),
  redige_par    uuid NOT NULL REFERENCES membre (id),
  redige_le     date NOT NULL,
  UNIQUE (id, redige_par),
  UNIQUE (id, valeur)
);

CREATE INDEX statut_engagement ON statut (engagement_id);

CREATE TABLE statut_preuve (
  statut_id bigint NOT NULL REFERENCES statut (id),
  source_id bigint NOT NULL REFERENCES source (id),
  PRIMARY KEY (statut_id, source_id)
);

-- Proposé : seul le rédacteur propose son brouillon (la clé étrangère
-- composée l'impose).
CREATE TABLE statut_proposition (
  statut_id  bigint PRIMARY KEY,
  redige_par uuid NOT NULL,
  propose_le date NOT NULL,
  FOREIGN KEY (statut_id, redige_par) REFERENCES statut (id, redige_par)
);

-- Validé : par un relecteur distinct du rédacteur (T7).
CREATE TABLE statut_validation (
  statut_id  bigint PRIMARY KEY REFERENCES statut_proposition (statut_id),
  redige_par uuid NOT NULL,
  valide_par uuid NOT NULL REFERENCES membre (id),
  valide_le  date NOT NULL,
  FOREIGN KEY (statut_id, redige_par) REFERENCES statut (id, redige_par),
  CONSTRAINT statut_quatre_yeux CHECK (valide_par <> redige_par)
);

-- Rejeté : le relecteur renvoie la proposition, avec un motif ; le circuit
-- s'arrête là, le rédacteur rédige un nouveau statut.
CREATE TABLE statut_rejet (
  statut_id  bigint PRIMARY KEY REFERENCES statut_proposition (statut_id),
  redige_par uuid NOT NULL,
  rejete_par uuid NOT NULL REFERENCES membre (id),
  rejete_le  date NOT NULL,
  motif      text NOT NULL CHECK (char_length(motif) BETWEEN 1 AND 2000),
  FOREIGN KEY (statut_id, redige_par) REFERENCES statut (id, redige_par),
  CONSTRAINT statut_rejet_quatre_yeux CHECK (rejete_par <> redige_par)
);

-- Publié : seulement après validation, avec l'entrée du journal public
-- écrite dans la même transaction. Sans préavis (ticket #14), seuls « tenu »
-- et « en cours » sont publiables : la valeur est recopiée, vérifiée par une
-- clé étrangère composée, et contrainte.
CREATE TABLE statut_publication (
  statut_id      bigint PRIMARY KEY REFERENCES statut_validation (statut_id),
  valeur         text NOT NULL,
  publie_par     uuid NOT NULL REFERENCES membre (id),
  publie_le      date NOT NULL,
  journal_numero bigint NOT NULL UNIQUE,
  FOREIGN KEY (statut_id, valeur) REFERENCES statut (id, valeur),
  CONSTRAINT statut_publiable_sans_preavis CHECK (valeur IN ('tenu', 'en_cours'))
);

-- Règles du circuit qui portent sur plusieurs tables.
CREATE FUNCTION observatoire_controle() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  CASE TG_TABLE_NAME
  WHEN 'engagement_source' THEN
    -- Les sources d'un engagement sont fixées à sa validation.
    IF EXISTS (SELECT 1 FROM engagement_validation WHERE engagement_id = NEW.engagement_id) THEN
      RAISE EXCEPTION 'observatoire : l''engagement % est validé, ses sources ne changent plus', NEW.engagement_id
        USING ERRCODE = 'check_violation';
    END IF;
  WHEN 'engagement_validation' THEN
    IF NOT EXISTS (SELECT 1 FROM engagement_source WHERE engagement_id = NEW.engagement_id) THEN
      RAISE EXCEPTION 'observatoire : l''engagement % n''a aucune source', NEW.engagement_id
        USING ERRCODE = 'check_violation';
    END IF;
  WHEN 'statut_preuve' THEN
    -- Les preuves d'un statut sont fixées à sa proposition.
    IF EXISTS (SELECT 1 FROM statut_proposition WHERE statut_id = NEW.statut_id) THEN
      RAISE EXCEPTION 'observatoire : le statut % est proposé, ses preuves ne changent plus', NEW.statut_id
        USING ERRCODE = 'check_violation';
    END IF;
  WHEN 'statut_proposition' THEN
    IF NOT EXISTS (SELECT 1 FROM statut_preuve WHERE statut_id = NEW.statut_id) THEN
      RAISE EXCEPTION 'observatoire : le statut % n''a aucune preuve', NEW.statut_id
        USING ERRCODE = 'check_violation';
    END IF;
  WHEN 'statut_validation' THEN
    IF EXISTS (SELECT 1 FROM statut_rejet WHERE statut_id = NEW.statut_id) THEN
      RAISE EXCEPTION 'observatoire : le statut % a été rejeté', NEW.statut_id
        USING ERRCODE = 'check_violation';
    END IF;
  WHEN 'statut_rejet' THEN
    IF EXISTS (SELECT 1 FROM statut_validation WHERE statut_id = NEW.statut_id) THEN
      RAISE EXCEPTION 'observatoire : le statut % est déjà validé', NEW.statut_id
        USING ERRCODE = 'check_violation';
    END IF;
  WHEN 'statut_publication' THEN
    -- L'entrée du journal public existe (même transaction) et est bien une
    -- publication de statut. (Un déclencheur plutôt qu'une clé étrangère,
    -- comme pour l'ancrage : celle-ci changerait le refus de TRUNCATE sur
    -- journal_public.)
    IF NOT EXISTS (SELECT 1 FROM journal_public WHERE numero = NEW.journal_numero AND type = 'statut_publie') THEN
      RAISE EXCEPTION 'observatoire : l''entrée % du journal public n''est pas une publication de statut', NEW.journal_numero
        USING ERRCODE = 'foreign_key_violation';
    END IF;
  END CASE;
  RETURN NEW;
END
$$;

CREATE TRIGGER engagement_source_controle BEFORE INSERT ON engagement_source
  FOR EACH ROW EXECUTE FUNCTION observatoire_controle();
CREATE TRIGGER engagement_validation_controle BEFORE INSERT ON engagement_validation
  FOR EACH ROW EXECUTE FUNCTION observatoire_controle();
CREATE TRIGGER statut_preuve_controle BEFORE INSERT ON statut_preuve
  FOR EACH ROW EXECUTE FUNCTION observatoire_controle();
CREATE TRIGGER statut_proposition_controle BEFORE INSERT ON statut_proposition
  FOR EACH ROW EXECUTE FUNCTION observatoire_controle();
CREATE TRIGGER statut_validation_controle BEFORE INSERT ON statut_validation
  FOR EACH ROW EXECUTE FUNCTION observatoire_controle();
CREATE TRIGGER statut_rejet_controle BEFORE INSERT ON statut_rejet
  FOR EACH ROW EXECUTE FUNCTION observatoire_controle();
CREATE TRIGGER statut_publication_controle BEFORE INSERT ON statut_publication
  FOR EACH ROW EXECUTE FUNCTION observatoire_controle();

-- Ajout seul, pour tous les rôles, propriétaire compris (T8).
CREATE FUNCTION observatoire_ajout_seul() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'observatoire en ajout seul : % refusé sur %', TG_OP, TG_TABLE_NAME
    USING ERRCODE = 'insufficient_privilege';
END
$$;

DO $$
DECLARE
  t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['elu', 'source', 'engagement', 'engagement_source', 'engagement_validation',
                           'statut', 'statut_preuve', 'statut_proposition', 'statut_validation',
                           'statut_rejet', 'statut_publication'] LOOP
    EXECUTE format('CREATE TRIGGER %I BEFORE UPDATE OR DELETE ON %I
                    FOR EACH ROW EXECUTE FUNCTION observatoire_ajout_seul()', t || '_sans_modification', t);
    EXECUTE format('CREATE TRIGGER %I BEFORE TRUNCATE ON %I
                    FOR EACH STATEMENT EXECUTE FUNCTION observatoire_ajout_seul()', t || '_sans_vidage', t);
    EXECUTE format('REVOKE UPDATE, DELETE, TRUNCATE ON %I FROM PUBLIC', t);
  END LOOP;
END
$$;
