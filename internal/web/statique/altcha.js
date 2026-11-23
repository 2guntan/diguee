// Captcha ALTCHA (spec §4, étape 2 ; ADR 0007), écrit dans le dépôt, sans
// bibliothèque tierce. Le défi est dans la page (attributs data-altcha-*) :
// on cherche le nombre n de 0 à maxnumber tel que SHA-256(sel + n) égale le
// challenge, puis on place la solution (JSON en base64, format ALTCHA) dans
// le champ caché « altcha ». Le serveur vérifie signature, expiration,
// solution et usage unique. Sans ce script, le formulaire mène à la page qui
// explique comment passer par le SMS.
(function () {
  "use strict";
  var f = document.querySelector("form[data-altcha-defi]");
  if (!f) { return; }
  var d = f.dataset;
  var champ = f.querySelector('input[name="altcha"]');
  var bouton = f.querySelector('button[type="submit"]');
  var etat = document.getElementById("altcha-etat");
  function dire(t) { if (etat) { etat.textContent = t; } }
  function echec() { bouton.disabled = false; dire(d.altchaImpossible); }
  if (!champ || !bouton || !window.crypto || !crypto.subtle || !window.TextEncoder || !window.Promise) {
    dire(d.altchaImpossible);
    return;
  }
  var encodeur = new TextEncoder();
  var max = parseInt(d.altchaMax, 10);
  var debut = Date.now();
  var LOT = 500;
  function hex(tampon) {
    var o = new Uint8Array(tampon), s = "";
    for (var i = 0; i < o.length; i++) { s += (o[i] < 16 ? "0" : "") + o[i].toString(16); }
    return s;
  }
  function fini(n) {
    champ.value = btoa(JSON.stringify({
      algorithm: d.altchaAlgorithme, challenge: d.altchaDefi, number: n,
      salt: d.altchaSel, signature: d.altchaSignature, took: Date.now() - debut
    }));
    bouton.disabled = false;
    dire(d.altchaPret);
  }
  function tranche(premier) {
    if (premier > max) { echec(); return; }
    var dernier = Math.min(premier + LOT - 1, max), calculs = [];
    for (var n = premier; n <= dernier; n++) {
      calculs.push(crypto.subtle.digest("SHA-256", encodeur.encode(d.altchaSel + n)));
    }
    Promise.all(calculs).then(function (hs) {
      for (var i = 0; i < hs.length; i++) {
        if (hex(hs[i]) === d.altchaDefi) { fini(premier + i); return; }
      }
      tranche(dernier + 1);
    }, echec);
  }
  bouton.disabled = true;
  dire(d.altchaCalcul);
  tranche(0);
})();
