-- Espace équipe (spec §6.4 et §9, ADR 0008) : comptes nominatifs et audit
-- interne des actions sur les comptes.
--
-- Comme ailleurs, aucune colonne n'est plus fine que le jour : les sessions,
-- l'anti-rejeu des codes et le compte des échecs de connexion vivent en
-- mémoire seulement. Aucune adresse IP n'est stockée.

CREATE TABLE membre (
  id                  uuid PRIMARY KEY,
  identifiant         text NOT NULL UNIQUE CHECK (identifiant ~ '^[a-z0-9][a-z0-9._-]{1,39}$'),
  nom                 text NOT NULL CHECK (char_length(nom) BETWEEN 1 AND 100),
  roles               text[] NOT NULL CHECK (cardinality(roles) >= 1
                        AND roles <@ ARRAY['redacteur', 'relecteur', 'administrateur']),
  mot_de_passe        text NOT NULL CHECK (mot_de_passe LIKE 'pbkdf2-sha256$%'),  -- empreinte, jamais le mot de passe
  totp_secret_chiffre bytea NOT NULL,                                        -- AES-256-GCM (DIGUEE_CLE_TOTP)
  actif               boolean NOT NULL DEFAULT true
);

-- Un compte n'est jamais supprimé (les statuts de l'observatoire citeront
-- leur rédacteur et leur relecteur), et une désactivation est définitive.
CREATE FUNCTION membre_controle() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION 'membre : suppression refusée, désactiver le compte'
      USING ERRCODE = 'insufficient_privilege';
  END IF;
  IF NOT OLD.actif AND NEW.actif THEN
    RAISE EXCEPTION 'membre : un compte désactivé ne peut pas être réactivé'
      USING ERRCODE = 'check_violation';
  END IF;
  IF NEW.id <> OLD.id OR NEW.identifiant <> OLD.identifiant THEN
    RAISE EXCEPTION 'membre : identifiant non modifiable'
      USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END
$$;

CREATE TRIGGER membre_sans_suppression
  BEFORE UPDATE OR DELETE ON membre
  FOR EACH ROW EXECUTE FUNCTION membre_controle();

-- Audit interne : qui a fait quoi sur quel compte, au jour près. Ces actions
-- ne sont pas publiques, donc pas au journal public (ADR 0010), mais la table
-- est elle aussi en ajout seul.
CREATE TABLE audit_equipe (
  numero bigint PRIMARY KEY CHECK (numero > 0),
  jour   date NOT NULL,
  type   text NOT NULL CHECK (type IN ('creation', 'creation_commande', 'desactivation',
                                       'changement_roles', 'reinitialisation_acces', 'connexion')),
  acteur uuid REFERENCES membre (id),          -- NULL : commande diguee creer-membre
  cible  uuid NOT NULL REFERENCES membre (id),
  roles  text[],                               -- rôles après l'action
  CHECK ((acteur IS NULL) = (type = 'creation_commande'))
);

CREATE FUNCTION audit_equipe_ajout_seul() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'audit de l''équipe en ajout seul : % refusé', TG_OP
    USING ERRCODE = 'insufficient_privilege';
END
$$;

CREATE TRIGGER audit_equipe_sans_modification
  BEFORE UPDATE OR DELETE ON audit_equipe
  FOR EACH ROW EXECUTE FUNCTION audit_equipe_ajout_seul();

CREATE TRIGGER audit_equipe_sans_vidage
  BEFORE TRUNCATE ON audit_equipe
  FOR EACH STATEMENT EXECUTE FUNCTION audit_equipe_ajout_seul();
