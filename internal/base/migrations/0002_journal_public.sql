-- Journal public (spec §10, ADR 0010) : entrées chaînées, en ajout seul.
--
-- C'est la seule table qui porte une heure (à la minute) : ses entrées sont
-- publiques et ne sont jamais écrites à cause d'un envoi (paquet journal).
CREATE TABLE journal_public (
  numero               bigint PRIMARY KEY CHECK (numero > 0),
  horodatage           timestamptz NOT NULL
                         CHECK (extract(epoch FROM horodatage) % 60 = 0),  -- à la minute
  type                 text NOT NULL CHECK (type ~ '^[a-z][a-z_]*$'),
  contenu              text NOT NULL                      -- JSON canonique, octet pour octet
                         CHECK (jsonb_typeof(contenu::jsonb) = 'object'),
  empreinte_precedente bytea NOT NULL UNIQUE CHECK (octet_length(empreinte_precedente) = 32),
  empreinte            bytea NOT NULL UNIQUE CHECK (octet_length(empreinte) = 32)
);

CREATE INDEX journal_public_type ON journal_public (type, numero);

-- Ajout seul, pour tous les rôles, propriétaire compris : toute modification
-- ou suppression échoue.
CREATE FUNCTION journal_public_ajout_seul() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'journal public en ajout seul : % refusé', TG_OP
    USING ERRCODE = 'insufficient_privilege';
END
$$;

CREATE TRIGGER journal_public_sans_modification
  BEFORE UPDATE OR DELETE ON journal_public
  FOR EACH ROW EXECUTE FUNCTION journal_public_ajout_seul();

CREATE TRIGGER journal_public_sans_vidage
  BEFORE TRUNCATE ON journal_public
  FOR EACH STATEMENT EXECUTE FUNCTION journal_public_ajout_seul();

-- Chaque nouvelle entrée suit la dernière : numéro suivant, empreinte
-- précédente égale à celle de la dernière entrée (32 octets nuls pour la
-- première), horodatage qui ne recule pas. Les empreintes elles-mêmes sont
-- recalculées par le script de vérification.
CREATE FUNCTION journal_public_controle_chaine() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
  prec journal_public%ROWTYPE;
BEGIN
  SELECT * INTO prec FROM journal_public ORDER BY numero DESC LIMIT 1;
  IF NOT FOUND THEN
    IF NEW.numero <> 1 OR NEW.empreinte_precedente <> decode(repeat('00', 32), 'hex') THEN
      RAISE EXCEPTION 'journal public : la première entrée doit avoir le numéro 1 et une empreinte précédente nulle'
        USING ERRCODE = 'check_violation';
    END IF;
  ELSIF NEW.numero <> prec.numero + 1
     OR NEW.empreinte_precedente <> prec.empreinte
     OR NEW.horodatage < prec.horodatage THEN
    RAISE EXCEPTION 'journal public : l''entrée % ne suit pas l''entrée %', NEW.numero, prec.numero
      USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END
$$;

CREATE TRIGGER journal_public_chaine
  BEFORE INSERT ON journal_public
  FOR EACH ROW EXECUTE FUNCTION journal_public_controle_chaine();

REVOKE UPDATE, DELETE, TRUNCATE ON journal_public FROM PUBLIC;
