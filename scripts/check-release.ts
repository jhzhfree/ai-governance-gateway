for (const name of ['NODE_IMAGE', 'HIGRESS_IMAGE']) {
  const value = process.env[name] || '';
  if (!/^\S+@sha256:[a-f0-9]{64}$/.test(value)) throw new Error(name + ' must be pinned by sha256 digest');
}
process.stdout.write('Image references are digest-pinned. This checks format only, not registry authenticity or CVEs.\n');
