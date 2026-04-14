import { sha256 } from 'crypto-hash'

const sleep = (ms) => new Promise(resolve => setTimeout(resolve, ms))

export async function findProofOfWork (data, difficulty, allowedDigits) {
  allowedDigits = allowedDigits || '012def'
  let nonce = 0
  while (nonce < 1000000) {
    const hash = await sha256(data + nonce)

    // console.debug('findProofOfWork', hash)
    let found = true
    for (let i = 0; i < difficulty; i++) {
      // console.debug('findProofOfWork', i, hash.charAt(i), '0123def'.includes(hash.charAt(i)))
      if (allowedDigits.includes(hash.charAt(i)) !== true) {
        found = false
        break
      }
    }
    if (found) {
      // console.debug('found solution', nonce, hash)
      return nonce
    }
    nonce++
    if (nonce % 10000 === 0) {
      // free the main thread a bit
      await sleep(0)
    }
  }
  return -1
}

export function decodePowConfig (headerValue) {
  const parts = headerValue.split(';').map(p => p.trim())
  const config = {}
  parts.forEach(part => {
    const [key, val] = part.split('=').map(s => s.trim())
    if (key && val) {
      if (key === 'difficulty') {
        config[key] = parseInt(val, 10)
      } else {
        config[key] = val
      }
    }
  })
  return config
}

export async function fetchWithPOW (input, init = {}) {
  // First attempt
  let response

  init = init || {}
  // init.method = 'HEAD'

  try {
    response = await fetch(input, init)
  } catch (e) {
    console.debug('powgo fetch exception', e)
  }
  if (response.status !== 299 && response.status !== 403 && response.status !== 429) {
    return response
  }
  const configHeader = response.headers.get('x-powgo-config')
  if (!configHeader) {
    return response
  }
  const config = decodePowConfig(configHeader)
  // console.debug('powgo: received config header', configHeader, config)
  const { session, difficulty: diff, allowed } = config
  if (!session || !diff) {
    return response
  }
  // Check localStorage for cached nonce
  const storageKey = `pow:${session}:${diff}:${allowed}`
  const cachedNonce = localStorage.getItem(storageKey)
  if (cachedNonce) {
    const newInit = { ...init }
    newInit.headers = newInit.headers ? new Headers(newInit.headers) : new Headers()
    newInit.headers.set('x-powgo-nonce', cachedNonce)
    response = await fetch(input, newInit)
    if (response.status !== 403 && response.status !== 429) {
      return response
    }
    // Cached nonce didn't work, remove it
    localStorage.removeItem(storageKey)
  }
  // Compute nonce
  const nonce = await findProofOfWork(session, diff, allowed)
  if (nonce < 0) {
    console.warn('Failed to find proof-of-work nonce')
    return response
  }
  // Retry with nonce header
  const newInit = { ...init }
  newInit.headers = newInit.headers ? new Headers(newInit.headers) : new Headers()
  newInit.headers.set('x-powgo-nonce', nonce.toString())
  response = await fetch(input, newInit)
  // If success, store nonce in localStorage for future use
  if (response.ok) {
    localStorage.setItem(storageKey, nonce.toString())
  }
  return response
}

/*
// Example with node
const difficulty = 11

let data
let result
data = crypto.randomUUID()
console.time('solver')
result = await findProofOfWork(data, difficulty);
console.timeEnd('solver')
if (result >= 0) {
    console.log(`uuid: ${data}, result: ${result}`);
} else {
    console.log('failed to find a solution in a reasonable time')
}
*/
