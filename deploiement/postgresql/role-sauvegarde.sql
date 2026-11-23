-- Rôle des sauvegardes (ticket #15). À lancer une fois, en
-- superutilisateur, après roles.sql (preparer-vps.sh postgresql le fait) :
--   sudo -u postgres psql -v ON_ERROR_STOP=1 -f role-sauvegarde.sql
--
--   diguee_sauvegarde : rôle de connexion de pg_dump (utilisateur système
--                       diguee-sauvegarde, « peer »), membre du rôle prédéfini
--                       pg_read_all_data : lecture de toutes les tables et
--                       séquences, aucune écriture, aucun objet à lui.
CREATE ROLE diguee_sauvegarde LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS
  IN ROLE pg_read_all_data;
-- pg_dump n'ouvre qu'une connexion.
ALTER ROLE diguee_sauvegarde CONNECTION LIMIT 2;
GRANT CONNECT ON DATABASE diguee TO diguee_sauvegarde;
