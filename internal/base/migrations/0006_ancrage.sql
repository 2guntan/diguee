-- Ancrage extérieur du journal public (spec §10, ADR 0010, ticket #12) : ce
-- qui a été ancré, jour par jour, en ajout seul comme le journal.
--
-- Les lignes ne sont datées qu'au jour. Elles ne contiennent que des données
-- publiques : l'empreinte de tête du journal, la preuve OpenTimestamps et
-- les liens des copies archivées sur archive.org.

-- Une preuve n'est jamais modifiée : sa mise à niveau (attestation Bitcoin)
-- est une révision de plus.
CREATE TABLE ancrage_preuve (
  jour        date NOT NULL,
  revision    smallint NOT NULL CHECK (revision >= 0),  -- 0 : engagements des calendriers
  numero      bigint NOT NULL,                          -- entrée de tête ancrée
  empreinte   bytea NOT NULL CHECK (octet_length(empreinte) = 32),
  ots         bytea NOT NULL CHECK (octet_length(ots) BETWEEN 32 AND 65536),  -- fichier .ots
  calendriers smallint NOT NULL CHECK (calendriers >= 1),
  bitcoin     boolean NOT NULL,                         -- la preuve contient une attestation Bitcoin
  PRIMARY KEY (jour, revision)
);

-- La tête ancrée doit être une entrée du journal, avec son empreinte. (Un
-- déclencheur plutôt qu'une clé étrangère : celle-ci changerait le refus de
-- TRUNCATE sur journal_public.)
CREATE FUNCTION ancrage_preuve_tete() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM journal_public WHERE numero = NEW.numero AND empreinte = NEW.empreinte) THEN
    RAISE EXCEPTION 'ancrage : l''entrée % du journal public n''a pas cette empreinte', NEW.numero
      USING ERRCODE = 'foreign_key_violation';
  END IF;
  RETURN NEW;
END
$$;

CREATE TRIGGER ancrage_preuve_tete_du_journal
  BEFORE INSERT ON ancrage_preuve
  FOR EACH ROW EXECUTE FUNCTION ancrage_preuve_tete();

-- Dépôts sur archive.org : une ligne par jour et par page.
CREATE TABLE ancrage_archive (
  jour  date NOT NULL,
  cible text NOT NULL CHECK (cible IN ('journal', 'journal.json')),
  lien  text NOT NULL CHECK (lien LIKE 'https://%' AND length(lien) <= 2048),
  PRIMARY KEY (jour, cible)
);

CREATE FUNCTION ancrage_ajout_seul() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'ancrage en ajout seul : % refusé', TG_OP
    USING ERRCODE = 'insufficient_privilege';
END
$$;

CREATE TRIGGER ancrage_preuve_sans_modification
  BEFORE UPDATE OR DELETE ON ancrage_preuve
  FOR EACH ROW EXECUTE FUNCTION ancrage_ajout_seul();
CREATE TRIGGER ancrage_preuve_sans_vidage
  BEFORE TRUNCATE ON ancrage_preuve
  FOR EACH STATEMENT EXECUTE FUNCTION ancrage_ajout_seul();
CREATE TRIGGER ancrage_archive_sans_modification
  BEFORE UPDATE OR DELETE ON ancrage_archive
  FOR EACH ROW EXECUTE FUNCTION ancrage_ajout_seul();
CREATE TRIGGER ancrage_archive_sans_vidage
  BEFORE TRUNCATE ON ancrage_archive
  FOR EACH STATEMENT EXECUTE FUNCTION ancrage_ajout_seul();

REVOKE UPDATE, DELETE, TRUNCATE ON ancrage_preuve, ancrage_archive FROM PUBLIC;
