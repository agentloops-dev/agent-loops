// Every package the web remote is built from carries a licence we can ship (research R9).
import { licenceOf, webPackages } from "./lockfile.mjs";

// PSF-2.0 is argparse, which markdown-it needs only for its command line; it is never bundled.
const allowed = /^(MIT|ISC|BSD-2-Clause|BSD-3-Clause|0BSD|Apache-2\.0|PSF-2\.0)$/;
const packages = webPackages().filter(({ key }) => licenceOf(key) !== "not installed");
const refused = packages
  .map(({ key }) => [key, licenceOf(key)])
  .filter(([, licence]) => !allowed.test(licence ?? ""))
  .map(([key, licence]) => `${key}: ${licence ?? "no licence"}`);
if (refused.length) {
  console.error("licences: not on the list (MIT, ISC, BSD, Apache-2.0, PSF-2.0):\n" + refused.join("\n"));
  process.exit(1);
}
console.log(`licences: ${packages.length} packages, all allowed`);
