-- Préavis et droit de réponse (spec §9, ADR 0008, ticket #14), en ajout
-- seul comme le reste de l'observatoire (migration 0007).
--
--   statut_validation (non tenu ou partiel)
--     ─► statut_preavis (date d'envoi du dossier par un membre, échéance)
--          ─► statut_publication, à partir de l'échéance seulement
--          └► droit_reponse (saisi par un membre, avec son canal de réception)
--               ─► droit_reponse_verification (par un autre membre)
--                    ─► droit_reponse_publication (+ journal public), avec
--                       le statut ou, s'il est déjà publié, dès la vérification
--               └► droit_reponse_rejet (par un autre membre, avec un motif)
--
-- La plateforme n'envoie rien : elle prépare le dossier (PDF), un membre
-- l'envoie lui-même par le canal officiel de l'élu, puis enregistre la date
-- d'envoi. L'échéance est calculée par l'application (DIGUEE_PREAVIS_JOURS)
-- et conservée ; la base exige qu'elle soit au moins 15 jours après l'envoi
-- (ADR 0008), et qu'une publication de « non tenu » ou « partiel » ait lieu
-- à l'échéance ou après.
--
-- Comme ailleurs (T1), les dates sont au jour, les identifiants attribués
-- par l'application, sans séquence.

-- Le préavis remplace la contrainte du ticket #13, qui interdisait toute
-- publication de « non tenu » et de « partiel ».
ALTER TABLE statut_publication DROP CONSTRAINT statut_publiable_sans_preavis;

CREATE TABLE statut_preavis (
  statut_id      bigint PRIMARY KEY REFERENCES statut_validation (statut_id),
  valeur         text NOT NULL CHECK (valeur IN ('partiel', 'non_tenu')),
  envoye_le      date NOT NULL,
  echeance       date NOT NULL,
  moyen          text NOT NULL CHECK (char_length(moyen) BETWEEN 1 AND 500),  -- comment le dossier a été envoyé
  dossier_sha256 bytea NOT NULL CHECK (octet_length(dossier_sha256) = 32),   -- le dossier PDF envoyé
  enregistre_par uuid NOT NULL REFERENCES membre (id),
  enregistre_le  date NOT NULL CHECK (enregistre_le >= envoye_le),
  FOREIGN KEY (statut_id, valeur) REFERENCES statut (id, valeur),
  CONSTRAINT statut_preavis_quinze_jours CHECK (echeance >= envoye_le + 15)
);

-- Un droit de réponse de l'élu, reçu par un canal vérifiable, recopié tel
-- quel par le membre qui l'a reçu. Il porte sur un statut qui a fait l'objet
-- d'un préavis.
CREATE TABLE droit_reponse (
  id           bigint PRIMARY KEY CHECK (id > 0),
  statut_id    bigint NOT NULL REFERENCES statut_preavis (statut_id),
  texte        text NOT NULL CHECK (char_length(texte) BETWEEN 1 AND 10000),
  canal        text NOT NULL CHECK (canal IN ('adresse_officielle', 'courrier_signe', 'compte_officiel')),
  canal_detail text NOT NULL CHECK (char_length(canal_detail) BETWEEN 1 AND 500),  -- jamais publié
  recu_le      date NOT NULL,
  saisi_par    uuid NOT NULL REFERENCES membre (id),
  saisi_le     date NOT NULL CHECK (saisi_le >= recu_le),
  UNIQUE (id, saisi_par)
);

CREATE INDEX droit_reponse_statut ON droit_reponse (statut_id);

-- Vérifié : par un autre membre que celui qui l'a saisi (quatre yeux).
CREATE TABLE droit_reponse_verification (
  droit_reponse_id bigint PRIMARY KEY,
  saisi_par        uuid NOT NULL,
  verifie_par      uuid NOT NULL REFERENCES membre (id),
  verifie_le       date NOT NULL,
  FOREIGN KEY (droit_reponse_id, saisi_par) REFERENCES droit_reponse (id, saisi_par),
  CONSTRAINT droit_reponse_quatre_yeux CHECK (verifie_par <> saisi_par)
);

-- Rejeté : canal non vérifiable, par exemple ; par un autre membre, avec
-- un motif. Le texte n'est pas publié.
CREATE TABLE droit_reponse_rejet (
  droit_reponse_id bigint PRIMARY KEY,
  saisi_par        uuid NOT NULL,
  rejete_par       uuid NOT NULL REFERENCES membre (id),
  rejete_le        date NOT NULL,
  motif            text NOT NULL CHECK (char_length(motif) BETWEEN 1 AND 2000),
  FOREIGN KEY (droit_reponse_id, saisi_par) REFERENCES droit_reponse (id, saisi_par),
  CONSTRAINT droit_reponse_rejet_quatre_yeux CHECK (rejete_par <> saisi_par)
);

-- Publié : tel quel, avec l'entrée du journal public écrite dans la même
-- transaction, une fois le statut publié.
CREATE TABLE droit_reponse_publication (
  droit_reponse_id bigint PRIMARY KEY REFERENCES droit_reponse_verification (droit_reponse_id),
  publie_par       uuid NOT NULL REFERENCES membre (id),
  publie_le        date NOT NULL,
  journal_numero   bigint NOT NULL UNIQUE
);

CREATE FUNCTION observatoire_preavis_controle() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
  p statut_preavis%ROWTYPE;
  jour_journal date;
BEGIN
  CASE TG_TABLE_NAME
  WHEN 'statut_preavis' THEN
    IF NOT EXISTS (SELECT 1 FROM statut_validation WHERE statut_id = NEW.statut_id AND valide_le <= NEW.envoye_le) THEN
      RAISE EXCEPTION 'observatoire : le préavis du statut % précède sa validation', NEW.statut_id
        USING ERRCODE = 'check_violation', CONSTRAINT = 'statut_preavis_apres_validation';
    END IF;
  WHEN 'statut_publication' THEN
    IF NEW.valeur IN ('partiel', 'non_tenu') THEN
      -- T9 : pas de publication sans préavis échu. La date de publication est
      -- celle de l'entrée du journal public (chaînée, ancrée chaque jour).
      SELECT * INTO p FROM statut_preavis WHERE statut_id = NEW.statut_id;
      SELECT timezone('UTC', horodatage)::date INTO jour_journal FROM journal_public WHERE numero = NEW.journal_numero;
      IF p.statut_id IS NULL OR NEW.publie_le < p.echeance OR jour_journal IS DISTINCT FROM NEW.publie_le THEN
        RAISE EXCEPTION 'observatoire : le statut % demande un préavis échu avant sa publication', NEW.statut_id
          USING ERRCODE = 'check_violation', CONSTRAINT = 'statut_preavis_echu';
      END IF;
    END IF;
    -- Un droit de réponse reçu mais pas encore vérifié (ni rejeté) est
    -- publié avec le statut : on l'attend.
    IF EXISTS (SELECT 1 FROM droit_reponse d WHERE d.statut_id = NEW.statut_id
               AND NOT EXISTS (SELECT 1 FROM droit_reponse_verification v WHERE v.droit_reponse_id = d.id)
               AND NOT EXISTS (SELECT 1 FROM droit_reponse_rejet r WHERE r.droit_reponse_id = d.id)) THEN
      RAISE EXCEPTION 'observatoire : un droit de réponse au statut % attend sa vérification', NEW.statut_id
        USING ERRCODE = 'check_violation', CONSTRAINT = 'droit_reponse_en_attente';
    END IF;
  WHEN 'droit_reponse' THEN
    SELECT * INTO p FROM statut_preavis WHERE statut_id = NEW.statut_id;
    IF NEW.recu_le < p.envoye_le THEN
      RAISE EXCEPTION 'observatoire : un droit de réponse ne précède pas le préavis du statut %', NEW.statut_id
        USING ERRCODE = 'check_violation', CONSTRAINT = 'droit_reponse_apres_preavis';
    END IF;
  WHEN 'droit_reponse_verification' THEN
    IF EXISTS (SELECT 1 FROM droit_reponse_rejet WHERE droit_reponse_id = NEW.droit_reponse_id) THEN
      RAISE EXCEPTION 'observatoire : le droit de réponse % a été rejeté', NEW.droit_reponse_id
        USING ERRCODE = 'check_violation';
    END IF;
  WHEN 'droit_reponse_rejet' THEN
    IF EXISTS (SELECT 1 FROM droit_reponse_verification WHERE droit_reponse_id = NEW.droit_reponse_id) THEN
      RAISE EXCEPTION 'observatoire : le droit de réponse % est déjà vérifié', NEW.droit_reponse_id
        USING ERRCODE = 'check_violation';
    END IF;
  WHEN 'droit_reponse_publication' THEN
    IF NOT EXISTS (SELECT 1 FROM droit_reponse d JOIN statut_publication pu ON pu.statut_id = d.statut_id
                   WHERE d.id = NEW.droit_reponse_id) THEN
      RAISE EXCEPTION 'observatoire : le statut du droit de réponse % n''est pas publié', NEW.droit_reponse_id
        USING ERRCODE = 'check_violation';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM journal_public WHERE numero = NEW.journal_numero AND type = 'droit_de_reponse') THEN
      RAISE EXCEPTION 'observatoire : l''entrée % du journal public n''est pas un droit de réponse', NEW.journal_numero
        USING ERRCODE = 'foreign_key_violation';
    END IF;
  END CASE;
  RETURN NEW;
END
$$;

CREATE TRIGGER statut_preavis_controle BEFORE INSERT ON statut_preavis
  FOR EACH ROW EXECUTE FUNCTION observatoire_preavis_controle();
CREATE TRIGGER statut_publication_preavis BEFORE INSERT ON statut_publication
  FOR EACH ROW EXECUTE FUNCTION observatoire_preavis_controle();
CREATE TRIGGER droit_reponse_controle BEFORE INSERT ON droit_reponse
  FOR EACH ROW EXECUTE FUNCTION observatoire_preavis_controle();
CREATE TRIGGER droit_reponse_verification_controle BEFORE INSERT ON droit_reponse_verification
  FOR EACH ROW EXECUTE FUNCTION observatoire_preavis_controle();
CREATE TRIGGER droit_reponse_rejet_controle BEFORE INSERT ON droit_reponse_rejet
  FOR EACH ROW EXECUTE FUNCTION observatoire_preavis_controle();
CREATE TRIGGER droit_reponse_publication_controle BEFORE INSERT ON droit_reponse_publication
  FOR EACH ROW EXECUTE FUNCTION observatoire_preavis_controle();

-- Ajout seul, pour tous les rôles, propriétaire compris (T8).
DO $$
DECLARE
  t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['statut_preavis', 'droit_reponse', 'droit_reponse_verification',
                           'droit_reponse_rejet', 'droit_reponse_publication'] LOOP
    EXECUTE format('CREATE TRIGGER %I BEFORE UPDATE OR DELETE ON %I
                    FOR EACH ROW EXECUTE FUNCTION observatoire_ajout_seul()', t || '_sans_modification', t);
    EXECUTE format('CREATE TRIGGER %I BEFORE TRUNCATE ON %I
                    FOR EACH STATEMENT EXECUTE FUNCTION observatoire_ajout_seul()', t || '_sans_vidage', t);
    EXECUTE format('REVOKE UPDATE, DELETE, TRUNCATE ON %I FROM PUBLIC', t);
  END LOOP;
END
$$;
