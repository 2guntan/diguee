-- Ticket #10 : la réactivation manuelle du parcours SMS (coupe-circuit) par
-- un administrateur est inscrite à l'audit interne de l'équipe. Elle ne
-- porte sur aucun membre : la cible devient facultative pour ce seul type.
ALTER TABLE audit_equipe DROP CONSTRAINT audit_equipe_type_check;
ALTER TABLE audit_equipe ADD CONSTRAINT audit_equipe_type_check
  CHECK (type IN ('creation', 'creation_commande', 'desactivation',
                  'changement_roles', 'reinitialisation_acces', 'connexion', 'reactivation_sms'));
ALTER TABLE audit_equipe ALTER COLUMN cible DROP NOT NULL;
ALTER TABLE audit_equipe ADD CONSTRAINT audit_equipe_cible_check
  CHECK ((cible IS NULL) = (type = 'reactivation_sms'));
