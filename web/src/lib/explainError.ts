// Foutmeldingen die zeggen wat je moet doen (fase 41). De server geeft een
// korte fout; hier staat per soort fout wat er waarschijnlijk misging en de
// volgende stap, in gewone taal. Onbekende fouten krijgen een algemene stap.

export type ErrorHelp = { uitleg: string; doen: string };

const rules: { re: RegExp; help: ErrorHelp }[] = [
  {
    re: /agent te oud|protocol \d+ nodig|te oude agent/i,
    help: {
      uitleg: "De agent op die server is te oud voor deze functie.",
      doen: "Werk de agents bij via Instellingen → Agents bijwerken. Dat duurt een paar seconden per server en verandert verder niets.",
    },
  },
  {
    re: /tweestapsverificatie|totp_required|\btotp\b/i,
    help: {
      uitleg: "Voor deze actie is tweestapsverificatie nodig, omdat ze een productieomgeving raakt.",
      doen: "Zet tweestapsverificatie aan bij Instellingen → Tweestapsverificatie (een minuut werk) en probeer opnieuw.",
    },
  },
  {
    re: /tik ter bevestiging|needs_confirmation|ter bevestiging/i,
    help: {
      uitleg: "Dit is een prodcluster; ClusterForge vraagt een extra bevestiging zodat je niet per ongeluk iets breekt.",
      doen: "Typ de korte naam (slug) van het cluster in het bevestigingsveld, precies zoals ze er staat.",
    },
  },
  {
    re: /no space left|schijf.*vol|disk[_ ]full|bijna vol/i,
    help: {
      uitleg: "De schijf van de server is vol; dan lukt bijna niets meer.",
      doen: "Open de node, ga naar Schijfruimte, laat meten wat ruimte inneemt en maak veilig ruimte vrij. Probeer daarna opnieuw.",
    },
  },
  {
    re: /could not get lock|dpkg.*(lock|interrupted)|apt.*(bezet|lock)|unattended-upgr/i,
    help: {
      uitleg: "Op de server loopt al een installatie of update, en twee tegelijk kan niet.",
      doen: "Wacht een paar minuten en probeer opnieuw. Blijft het hangen, herstart de node.",
    },
  },
  {
    re: /loopt al|al bezig|wacht of loopt|busy|cluster_busy|already running/i,
    help: {
      uitleg: "Er loopt al een taak op deze server of dit cluster. ClusterForge doet er één tegelijk, zodat ze elkaar niet storen.",
      doen: "Volg de lopende taak bij Taken en probeer opnieuw als ze klaar is.",
    },
  },
  {
    re: /offline|antwoordt (al .* )?niet|geen antwoord|no responders|niet bereikbaar|unreachable|timeout|time-out|timed out|deadline exceeded/i,
    help: {
      uitleg: "ClusterForge kreeg geen antwoord van de server of dienst.",
      doen: "Kijk of de machine aan staat (Proxmox → Gasten) en of de agent draait (Nodes). Was hij net herstart, wacht dan een minuut en probeer opnieuw.",
    },
  },
  {
    re: /connection refused|verbinding geweigerd/i,
    help: {
      uitleg: "De server is bereikbaar, maar de dienst op die poort draait niet of een firewall houdt de verbinding tegen.",
      doen: "Kijk of de dienst draait (op de pagina van de node bij Services) en of de firewall de poort toelaat (Firewall).",
    },
  },
  {
    re: /authentication failed|\b401\b|unauthori[sz]ed|ongeldig token|invalid token|permission denied for/i,
    help: {
      uitleg: "Het wachtwoord of token voor deze koppeling klopt niet (meer), of het heeft te weinig rechten.",
      doen: "Open de koppeling (bijvoorbeeld Instellingen → Proxmox-koppeling) en vul een nieuw token in. Test daarna de verbinding.",
    },
  },
  {
    re: /geen rechten|forbidden|alleen een beheerder|alleen beheerders|admin nodig|\b403\b/i,
    help: {
      uitleg: "Je account mag dit niet; alleen een beheerder (admin) mag dit doen.",
      doen: "Vraag een beheerder om dit te doen, of om je account beheerder te maken.",
    },
  },
  {
    re: /name resolution|no such host|nxdomain|dns/i,
    help: {
      uitleg: "De naam van de server kon niet omgezet worden in een adres (DNS).",
      doen: "Kijk of de naam juist geschreven is, of gebruik het IP-adres. Werkt het internet op die server wel?",
    },
  },
  {
    re: /x509|certificate|certificaat|tls handshake/i,
    help: {
      uitleg: "Het certificaat (het slotje) van de andere kant is niet geldig of niet vertrouwd.",
      doen: "Is het een eigen server met een zelfgemaakt certificaat, voeg dan de vingerafdruk toe bij de koppeling. Anders: vernieuw het certificaat.",
    },
  },
  {
    re: /niet gevonden|not found|\b404\b|bestaat niet/i,
    help: {
      uitleg: "Wat je zoekt, bestaat niet (meer). Misschien is het net verwijderd.",
      doen: "Ververs de pagina. Staat het er nog, kijk dan in het Logboek wie het wijzigde.",
    },
  },
  {
    re: /onverwachte fout|internal|er ging iets mis|\b500\b|\b502\b|\b503\b/i,
    help: {
      uitleg: "Er ging iets mis in ClusterForge zelf.",
      doen: "Probeer opnieuw. Blijft het fout gaan, kijk dan in het Logboek of bij Taken wat er net gebeurde, of vraag het aan de Assistent.",
    },
  },
];

/** explainError geeft de uitleg bij een foutmelding, of null. */
export function explainError(message: string | null | undefined): ErrorHelp | null {
  if (!message) return null;
  for (const r of rules) {
    if (r.re.test(message)) return r.help;
  }
  return null;
}
