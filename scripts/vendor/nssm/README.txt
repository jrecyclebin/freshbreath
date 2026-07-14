NSSM (the Non-Sucking Service Manager), vendored.

Source:  https://nssm.cc/release/nssm-2.24.zip
Version: 2.24 (2014-08-31) - the last stable release; the project has had
         no functional changes since.
License: Public domain (https://nssm.cc/usage - GPLv2 as a fallback in
         jurisdictions that don't recognize public domain dedication).

sha256:
  win32/nssm.exe  472232ca821b5c2ef562ab07f53638bc2cc82eae84cea13fbe674d6022b6481c
  win64/nssm.exe  f689ee9af94b00e9e3f0bb072b34caaf207f32dcb4f5782fc9ca351df9a06c97

Vendored instead of fetched at build time because nssm.cc is a small,
personal site that intermittently 503s / rate-limits, and this build has
broken CI releases because of it. The binary is static and unmaintained, so
there's nothing to keep in sync by re-downloading it.
