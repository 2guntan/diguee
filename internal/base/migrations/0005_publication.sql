-- Publication quotidienne (spec §8, ticket #11) : un instantané figé par jour
-- de publication, en ajout seul comme le journal public.
--
-- L'instantané ne contient que des agrégats masqués et arrondis, et n'est daté
-- qu'au jour : ni heure, ni réponse individuelle (T1). Son empreinte SHA-256
-- est inscrite au journal public, dans la même transaction.
CREATE TABLE publication (
  jour              date PRIMARY KEY,                     -- jour de publication : réponses des jours antérieurs
  reponses          integer NOT NULL CHECK (reponses >= 0 AND reponses % 10 = 0),  -- arrondi à la dizaine
  resultats_publies boolean NOT NULL,
  instantane        text NOT NULL                         -- JSON, servi octet pour octet
                      CHECK (jsonb_typeof(instantane::jsonb) = 'object'),
  html              text NOT NULL,                        -- le même instantané mis en page
  empreinte         bytea NOT NULL UNIQUE CHECK (octet_length(empreinte) = 32)  -- SHA-256 de instantane
);

-- Un instantané publié ne change plus jamais : toute modification, suppression
-- ou vidage échoue, pour tous les rôles.
CREATE FUNCTION publication_ajout_seul() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'publication en ajout seul : % refusé', TG_OP
    USING ERRCODE = 'insufficient_privilege';
END
$$;

CREATE TRIGGER publication_sans_modification
  BEFORE UPDATE OR DELETE ON publication
  FOR EACH ROW EXECUTE FUNCTION publication_ajout_seul();

CREATE TRIGGER publication_sans_vidage
  BEFORE TRUNCATE ON publication
  FOR EACH STATEMENT EXECUTE FUNCTION publication_ajout_seul();

REVOKE UPDATE, DELETE, TRUNCATE ON publication FROM PUBLIC;
