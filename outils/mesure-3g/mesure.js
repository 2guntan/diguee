#!/usr/bin/env node
// Mesure « 3G simulée » de la plateforme (ticket #17, spec E9 et T13).
//
// Le script construit l'exécutable, le lance en local (mode développement,
// intégrations « fake »), puis pilote Chromium avec Playwright :
//
//   1. Accueil, cache vide, sur un réseau 3G simulé (750 kbit/s descendant,
//      250 kbit/s montant, 100 ms de latence) et un processeur ralenti 4 fois
//      (téléphone d'entrée de gamme) : temps jusqu'à la fin de l'analyse du
//      HTML (DOMContentLoaded), premier rendu (FCP), plus grand rendu (LCP,
//      le titre) et polices chargées. Critère : accueil utilisable, soit
//      DOMContentLoaded et premier rendu, en moins de 3 s (médiane de 5
//      chargements). À titre indicatif, le même chargement en « Slow 3G »
//      (400 kbit/s, 400 ms).
//   2. JavaScript désactivé, même réseau : le parcours complet par le secours
//      SMS, de l'accueil à la page Merci, en suivant les liens et en
//      envoyant les formulaires ; le code est lu sur la page de développement
//      /dev/sms (mode fake).
//   3. Captures d'écran de l'accueil et des résultats (largeur 360 et 1280,
//      deviceScaleFactor 1) dans le dossier de sortie.
//
// À chaque étape, toute requête vers un autre hôte que le serveur local
// fait échouer la mesure.
//
// Prérequis : Go (go.mod), PostgreSQL 16 avec une base de développement,
// Node 18 ou plus et Playwright avec Chromium (hors du dépôt :
// `npm install -g playwright && npx playwright install chromium`).
//
// Lancement, depuis la racine du dépôt :
//
//   DIGUEE_POSTGRES_URL='postgres://localhost/diguee?sslmode=disable' \
//     node outils/mesure-3g/mesure.js [dossier de sortie]
//
// La base reçoit, comme avec `make lancer`, les migrations, l'entrée « mise
// en production » du journal public et une réponse de test (parcours SMS,
// numéro tiré au hasard). Ne jamais lancer ce script contre la base de
// production. Code de sortie 1 si un critère n'est pas tenu.

'use strict';

const { spawn, spawnSync } = require('node:child_process');
const fs = require('node:fs');
const net = require('node:net');
const os = require('node:os');
const path = require('node:path');
const { chromium } = require('playwright');

const racine = path.resolve(__dirname, '..', '..');
const sortie = path.resolve(process.argv[2] || path.join(os.tmpdir(), 'diguee-mesure-3g'));

// Profils réseau (débits en kbit/s, latence en ms) ; le premier sert au
// critère.
const PROFILS = [
  { nom: '3G', bas: 750, haut: 250, latence: 100 },
  { nom: 'Slow 3G (indicatif)', bas: 400, haut: 400, latence: 400 },
];
const RALENTISSEMENT_CPU = 4;
const CRITERE_MS = 3000;
const ESSAIS = 5;

function journal(...m) {
  console.log(...m);
}

function portLibre() {
  return new Promise((ok, ko) => {
    const s = net.createServer();
    s.once('error', ko);
    s.listen(0, '127.0.0.1', () => {
      const { port } = s.address();
      s.close(() => ok(port));
    });
  });
}

function jourIso(decalageJours) {
  return new Date(Date.now() + decalageJours * 86400000).toISOString().slice(0, 10);
}

async function attendreSante(url, delai) {
  const fin = Date.now() + delai;
  while (Date.now() < fin) {
    try {
      const r = await fetch(url + '/sante');
      if (r.ok) return;
    } catch {
      // pas encore prêt
    }
    await new Promise((ok) => setTimeout(ok, 200));
  }
  throw new Error('le serveur ne répond pas sur ' + url);
}

async function lancerServeur() {
  if (!process.env.DIGUEE_POSTGRES_URL) {
    throw new Error('DIGUEE_POSTGRES_URL est obligatoire (base de développement)');
  }
  fs.mkdirSync(sortie, { recursive: true });
  const binaire = path.join(sortie, 'diguee');
  const b = spawnSync('go', ['build', '-o', binaire, './cmd/diguee'], { cwd: racine, stdio: 'inherit' });
  if (b.status !== 0) throw new Error('go build a échoué');
  const port = await portLibre();
  const env = {
    ...process.env,
    DIGUEE_ENV: 'development',
    DIGUEE_TLS: 'non',
    DIGUEE_ADRESSE: '127.0.0.1:' + port,
    // Consultation ouverte depuis une semaine (sauf réglage contraire).
    DIGUEE_OUVERTURE: process.env.DIGUEE_OUVERTURE || jourIso(-7),
    DIGUEE_WHATSAPP: 'fake',
    DIGUEE_SMS: 'fake',
    DIGUEE_STOCKAGE_DOSSIER: path.join(sortie, 'archives'),
  };
  const log = fs.openSync(path.join(sortie, 'serveur.log'), 'w');
  const serveur = spawn(binaire, [], { env, stdio: ['ignore', log, log] });
  const url = 'http://127.0.0.1:' + port;
  await attendreSante(url, 30000);
  return { serveur, url };
}

// nouvellePage ouvre un contexte « téléphone » (360 × 740, échelle 1) avec
// le réseau simulé, sans cache, et note chaque hôte contacté.
async function nouvellePage(navigateur, profil, { javascript = true, cpu = true } = {}) {
  const contexte = await navigateur.newContext({
    viewport: { width: 360, height: 740 }, deviceScaleFactor: 1, isMobile: true, hasTouch: true,
    javaScriptEnabled: javascript, locale: 'fr-FR',
  });
  const page = await contexte.newPage();
  const cdp = await contexte.newCDPSession(page);
  await cdp.send('Network.enable');
  await cdp.send('Network.setCacheDisabled', { cacheDisabled: true });
  await cdp.send('Network.emulateNetworkConditions', {
    offline: false, latency: profil.latence,
    downloadThroughput: (profil.bas * 1000) / 8, uploadThroughput: (profil.haut * 1000) / 8,
    connectionType: 'cellular3g',
  });
  if (cpu) await cdp.send('Emulation.setCPUThrottlingRate', { rate: RALENTISSEMENT_CPU });
  const hotes = new Set();
  let octets = 0;
  page.on('request', (r) => hotes.add(new URL(r.url()).host));
  cdp.on('Network.loadingFinished', (e) => { octets += e.encodedDataLength; });
  return { contexte, page, hotes, octets: () => octets };
}

function verifierHotes(hotes, url, etape) {
  const attendu = new URL(url).host;
  const tiers = [...hotes].filter((h) => h !== attendu);
  if (tiers.length) throw new Error(`${etape} : requête vers un domaine tiers : ${tiers.join(', ')}`);
}

function mediane(xs) {
  const s = xs.filter(Number.isFinite).sort((a, b) => a - b);
  if (s.length === 0) return NaN;
  return s[Math.floor(s.length / 2)];
}

// mesurerAccueil charge l'accueil, cache vide, et lit les mesures du
// navigateur (Navigation Timing, Paint Timing, LCP, Resource Timing).
async function mesurerAccueil(navigateur, url, profil) {
  const { contexte, page, hotes, octets } = await nouvellePage(navigateur, profil);
  await page.goto(url + '/', { waitUntil: 'load', timeout: 60000 });
  const m = await page.evaluate(async () => {
    const nav = performance.getEntriesByType('navigation')[0];
    const fcp = performance.getEntriesByName('first-contentful-paint')[0];
    const lcp = await new Promise((ok) => {
      new PerformanceObserver((l) => {
        const es = l.getEntries();
        ok(es[es.length - 1]);
      }).observe({ type: 'largest-contentful-paint', buffered: true });
      setTimeout(() => ok(null), 2000);
    });
    await document.fonts.ready;
    const polices = performance.getEntriesByType('resource').filter((r) => r.name.endsWith('.woff2'));
    return {
      dcl: nav.domContentLoadedEventEnd,
      load: nav.loadEventEnd,
      fcp: fcp ? fcp.startTime : NaN,
      lcp: lcp ? lcp.startTime : NaN,
      lcpElement: lcp && lcp.element ? lcp.element.tagName.toLowerCase() + (lcp.element.className ? '.' + lcp.element.className : '') : '?',
      polices: polices.length ? Math.max(...polices.map((r) => r.responseEnd)) : 0,
      nbPolices: polices.length,
    };
  });
  // Il arrive que l'entrée « first-contentful-paint » manque : le plus
  // grand rendu (LCP) est alors le premier rendu connu.
  if (!Number.isFinite(m.fcp)) m.fcp = m.lcp;
  m.octets = octets();
  verifierHotes(hotes, url, 'accueil');
  await contexte.close();
  return m;
}

// parcoursSansJavaScript joue le parcours complet par le secours SMS, sans
// JavaScript, de l'accueil à la page Merci.
async function parcoursSansJavaScript(navigateur, url, profil) {
  const { contexte, page, hotes, octets } = await nouvellePage(navigateur, profil, { javascript: false, cpu: false });
  const etapes = [];
  let t0 = Date.now();
  const etape = async (nom, action, cheminAttendu) => {
    const debut = Date.now();
    await action();
    await page.waitForLoadState('load');
    const chemin = new URL(page.url()).pathname;
    if (cheminAttendu && chemin !== cheminAttendu) {
      throw new Error(`${nom} : arrivé sur ${chemin}, attendu ${cheminAttendu}\n` + (await page.locator('main').innerText()));
    }
    if ((await page.locator('script').count()) > 0 && !['/verifier', '/verifier/sms'].includes(chemin)) {
      throw new Error(`${nom} : script inattendu sur ${chemin}`);
    }
    etapes.push({ nom, chemin, ms: Date.now() - debut });
  };
  const numero = '+22177' + String(Math.floor(Math.random() * 1e7)).padStart(7, '0');
  await etape('accueil', () => page.goto(url + '/'), '/');
  await etape('vérifier mon numéro', () => page.click('main a[href="/verifier"]'), '/verifier');
  // Sans JavaScript, le bloc <noscript> mène à l'explication.
  await etape('« Que faire ? »', () => page.click('noscript a[href="/verifier/sans-javascript"], main a[href="/verifier/sans-javascript"]'), '/verifier/sans-javascript');
  await etape('vérifier par SMS', () => page.click('main a[href="/verifier/sms"]'), '/verifier/sms');
  await page.fill('input[name="numero"]', numero);
  await etape('envoi du numéro', () => page.click('form[action="/verifier/sms"] button[type="submit"]'), '/verifier/sms/saisie');
  await etape('page de développement /dev/sms', () => page.click('a[href="/dev/sms"]'), '/dev/sms');
  const texte = await page.locator('main').innerText();
  const m = texte.match(/\(mode de développement\)\s*:\s*(\d{6})/);
  if (!m) throw new Error('code absent de /dev/sms : ' + texte);
  await etape('retour à la saisie', () => page.click('a[href="/verifier/sms/saisie"]'), '/verifier/sms/saisie');
  await page.fill('input[name="code"]', m[1]);
  await etape('envoi du code', () => page.click('form[action="/verifier/sms/saisie"] button[type="submit"]'), '/questionnaire');
  // Une réponse par question : le premier choix des questions à choix unique.
  const vus = new Set();
  for (const radio of await page.locator('input[type="radio"]').all()) {
    const nom = await radio.getAttribute('name');
    if (!vus.has(nom)) {
      vus.add(nom);
      await radio.check();
    }
  }
  // Questions à choix multiples : « Je ne sais pas » (code 0, exclusif).
  for (const boite of await page.locator('input[type="checkbox"][value="0"]').all()) await boite.check();
  await page.check('input[name="majeur"]');
  await etape('récapitulatif', () => page.click('form[action="/questionnaire/recapitulatif"] button[type="submit"]:not([formaction])'), '/questionnaire/recapitulatif');
  if ((await page.locator('form[action="/envoyer"]').count()) === 0) {
    throw new Error('récapitulatif attendu :\n' + (await page.locator('main').innerText()));
  }
  await etape('réponse définitive', () => page.click('form[action="/envoyer"] button[type="submit"]:not([formaction])'), '/merci');
  const total = Date.now() - t0;
  verifierHotes(hotes, url, 'parcours sans JavaScript');
  await contexte.close();
  return { etapes, total, octets: octets() };
}

async function captures(navigateur, url) {
  const fichiers = [];
  for (const largeur of [360, 1280]) {
    const contexte = await navigateur.newContext({ viewport: { width: largeur, height: 800 }, deviceScaleFactor: 1 });
    const page = await contexte.newPage();
    for (const [nom, chemin] of [['accueil', '/'], ['resultats', '/resultats']]) {
      await page.goto(url + chemin, { waitUntil: 'load' });
      await page.evaluate(() => document.fonts.ready);
      const large = await page.evaluate(() => document.documentElement.scrollWidth);
      if (large > largeur) throw new Error(`${chemin} déborde à ${largeur} px de large (${large} px)`);
      const f = path.join(sortie, `${nom}-${largeur}.png`);
      await page.screenshot({ path: f, fullPage: true });
      fichiers.push(f);
    }
    await contexte.close();
  }
  return fichiers;
}

async function principal() {
  const { serveur, url } = await lancerServeur();
  let echec = false;
  const navigateur = await chromium.launch();
  try {
    journal(`Serveur : ${url} ; sortie : ${sortie}`);
    for (const [i, profil] of PROFILS.entries()) {
      const ms = [];
      for (let k = 0; k < ESSAIS; k++) ms.push(await mesurerAccueil(navigateur, url, profil));
      const med = (cle) => Math.round(mediane(ms.map((x) => x[cle])));
      const utilisable = Math.round(mediane(ms.map((x) => Math.max(x.dcl, x.fcp))));
      journal(`\nAccueil, ${profil.nom} (${profil.bas}/${profil.haut} kbit/s, ${profil.latence} ms, CPU ÷${RALENTISSEMENT_CPU}), médiane de ${ESSAIS} :`);
      journal(`  DOMContentLoaded ${med('dcl')} ms · premier rendu ${med('fcp')} ms · LCP ${med('lcp')} ms (${ms[0].lcpElement})`);
      journal(`  polices chargées ${med('polices')} ms (${ms[0].nbPolices} fichiers) · load ${med('load')} ms · ${ms[0].octets} octets transférés`);
      journal(`  utilisable (DOMContentLoaded et premier rendu) : ${utilisable} ms`);
      if (i === 0) {
        const ok = Number.isFinite(utilisable) && utilisable < CRITERE_MS;
        echec ||= !ok;
        journal(`  critère < ${CRITERE_MS} ms : ${ok ? 'tenu' : 'NON TENU'}`);
      }
    }
    const p = await parcoursSansJavaScript(navigateur, url, PROFILS[0]);
    journal(`\nParcours sans JavaScript par SMS, ${PROFILS[0].nom} : ${p.total} ms, ${p.octets} octets transférés`);
    for (const e of p.etapes) journal(`  ${e.nom.padEnd(32)} ${e.chemin.padEnd(28)} ${e.ms} ms`);
    journal('  arrivé sur la page Merci : oui');
    const fs2 = await captures(navigateur, url);
    journal('\nCaptures : ' + fs2.join(', '));
  } catch (e) {
    echec = true;
    console.error('\nÉCHEC : ' + (e && e.stack ? e.stack : e));
  } finally {
    await navigateur.close();
    serveur.kill('SIGTERM');
  }
  process.exitCode = echec ? 1 : 0;
}

principal();
