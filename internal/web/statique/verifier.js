// Vérification inversée (spec §5.1) : tant que la page affiche le jeton, on
// demande au serveur toutes les 3 secondes si le message WhatsApp est arrivé,
// puis on recharge la page, qui passe alors au questionnaire. Sans ce script,
// le bouton « J'ai envoyé le message » fait le même travail.
(function () {
  "use strict";
  function plusTard() { setTimeout(interroger, 3000); }
  function interroger() {
    fetch("/verifier/etat", { credentials: "same-origin", cache: "no-store" })
      .then(function (r) { return r.ok ? r.json() : { etat: "attente" }; })
      .then(function (d) {
        if (d && d.etat === "attente") { plusTard(); } else { location.replace("/verifier/whatsapp"); }
      })
      .catch(plusTard);
  }
  plusTard();
})();
