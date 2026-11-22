// Must stay in sync with pairs.Normalize in internal/pairs/pairs.go.
export const normalize = text =>
  text.trim().toLowerCase().replace(/[«»“”"]/g, '').replace(/[.!?]+$/, '').replace(/\s+/g, ' ').trim();

export const findPair = (pairs, mode, text) => {
  const wanted = normalize(text);
  return pairs.find(p => p.mode === mode && normalize(p.input) === wanted);
};

export const pickPair = (pairs, mode, exceptId, random = Math.random) => {
  const pool = pairs.filter(p => p.mode === mode && p.id !== exceptId);
  return pool.length ? pool[Math.floor(random() * pool.length)] : pairs.find(p => p.mode === mode);
};
