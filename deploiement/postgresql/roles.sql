-- Rôles et base de Diguee (ticket #7). À lancer une fois, en
-- superutilisateur : sudo -u postgres psql -v ON_ERROR_STOP=1 -f roles.sql
--
--   diguee_proprietaire : possède la base, le schéma et les tables ; ne sert
--                         qu'à « diguee migrer » (et à la Clôture, #16) ;
--   diguee_application  : rôle de groupe sans connexion, qui reçoit les
--                         droits de l'application (diguee migrer les
--                         accorde, internal/base/droits.go) ;
--   diguee_app          : rôle de connexion du serveur, membre de
--                         diguee_application et de rien d'autre.
-- Aucun mot de passe : connexion par le socket Unix en « peer »
-- (pg_hba.conf, pg_ident.conf).

CREATE ROLE diguee_proprietaire LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
CREATE ROLE diguee_application NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
CREATE ROLE diguee_app LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS INHERIT
  IN ROLE diguee_application;
-- Pas plus de 20 connexions pour le serveur (pool de pgx).
ALTER ROLE diguee_app CONNECTION LIMIT 20;

CREATE DATABASE diguee OWNER diguee_proprietaire ENCODING 'UTF8' LOCALE 'C.UTF-8' TEMPLATE template0;
REVOKE ALL ON DATABASE diguee FROM PUBLIC;
GRANT CONNECT ON DATABASE diguee TO diguee_app;
GRANT CONNECT, CREATE, TEMPORARY ON DATABASE diguee TO diguee_proprietaire;

\connect diguee
-- Le schéma public appartient au propriétaire ; personne d'autre n'y crée
-- rien. Les droits sur les tables viennent de « diguee migrer ».
ALTER SCHEMA public OWNER TO diguee_proprietaire;
REVOKE ALL ON SCHEMA public FROM PUBLIC;
GRANT USAGE ON SCHEMA public TO diguee_application;
-- Fonctions : seulement celles du propriétaire, appelées par ses
-- déclencheurs.
ALTER DEFAULT PRIVILEGES FOR ROLE diguee_proprietaire REVOKE EXECUTE ON FUNCTIONS FROM PUBLIC;
