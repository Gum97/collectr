import { describe, expect, it } from 'vitest'
import {
  maskEmail,
  maskIdentifier,
  maskOpaque,
  maskPhone,
  shortId,
  subjectLabel,
} from './mask'

describe('maskEmail', () => {
  it('keeps the first character and the domain', () => {
    expect(maskEmail('nam@gmail.com')).toBe('n***@gmail.com')
    expect(maskEmail('tuan@acme.vn')).toBe('t***@acme.vn')
    expect(maskEmail('linh@gmail.com')).toBe('l***@gmail.com')
  })

  it('reveals nothing when the local part is a single character', () => {
    expect(maskEmail('a@acme.vn')).toBe('***@acme.vn')
  })

  it('preserves case, because two addresses may differ only by it', () => {
    expect(maskEmail('Nam@Gmail.com')).toBe('N***@Gmail.com')
  })

  it('splits on the last @, so a local part containing one cannot leak', () => {
    expect(maskEmail('"a@b"@acme.vn')).toBe('"***@acme.vn')
  })

  it('trims surrounding whitespace rather than masking it', () => {
    expect(maskEmail('  nam@gmail.com  ')).toBe('n***@gmail.com')
  })

  it('falls back to the opaque mask when there is no @', () => {
    expect(maskEmail('nguyenvana')).toBe('n***')
  })
})

describe('maskPhone', () => {
  it('hides the two digits after the network prefix', () => {
    expect(maskPhone('0901234567')).toBe('09**234567')
  })

  it('ignores the separators people type', () => {
    expect(maskPhone('090 123 4567')).toBe('09**234567')
    expect(maskPhone('090-123-4567')).toBe('09**234567')
    expect(maskPhone('(090) 123.4567')).toBe('09**234567')
  })

  it('normalises country-coded numbers to one national form', () => {
    // Same subscriber, three spellings. Rendering them differently would put one
    // person into a queue as three cases.
    expect(maskPhone('+84901234567')).toBe('09**234567')
    expect(maskPhone('84902000111')).toBe('09**000111')
    expect(maskPhone('0902000111')).toBe('09**000111')
  })

  it('does not mistake an 84 inside a national number for a country code', () => {
    expect(maskPhone('0847000111')).toBe('08**000111')
    // Ten digits is a national number even when it happens to start with 84.
    expect(maskPhone('8470001110')).toBe('84**001110')
  })

  it('masks a value too short to reveal anything from', () => {
    expect(maskPhone('0901')).toBe('****')
    expect(maskPhone('090')).toBe('***')
  })
})

describe('maskIdentifier', () => {
  it('detects the shape when the API sends no kind', () => {
    expect(maskIdentifier('nam@gmail.com')).toBe('n***@gmail.com')
    expect(maskIdentifier('0901234567')).toBe('09**234567')
  })

  it('trusts the kind the API states over the shape', () => {
    // A phone number typed into an email field is still a phone number to the
    // subject table, and the mask must follow the record, not the string.
    expect(maskIdentifier('0901234567', 'phone')).toBe('09**234567')
    expect(maskIdentifier('nam@gmail.com', 'email')).toBe('n***@gmail.com')
  })

  it('returns null for an absent identifier instead of an empty label', () => {
    expect(maskIdentifier(null)).toBeNull()
    expect(maskIdentifier(undefined)).toBeNull()
    expect(maskIdentifier('   ')).toBeNull()
  })

  it('masks values of an unrecognised shape rather than passing them through', () => {
    expect(maskIdentifier('Nguyễn Văn A')).toBe('N***')
    expect(maskIdentifier('CMND 001099001234')).toBe('C***')
  })
})

describe('masking is idempotent', () => {
  // The API may start sending identifiers already masked. A second pass must
  // leave them alone rather than eat the part that is still useful.
  const samples = [
    'nam@gmail.com',
    'a@acme.vn',
    '0901234567',
    '+84901234567',
    'Nguyễn Văn A',
  ]

  for (const sample of samples) {
    it(`re-masking ${sample} changes nothing`, () => {
      const once = maskIdentifier(sample)
      expect(once).not.toBeNull()
      expect(maskIdentifier(once)).toBe(once)
    })
  }
})

describe('nothing escapes unmasked', () => {
  const samples = [
    'nam@gmail.com',
    'nguyen.van.a@acme.com.vn',
    '0901234567',
    '+84 90 123 4567',
    'Nguyễn Văn A',
    'CMND 001099001234',
  ]

  for (const sample of samples) {
    it(`${sample} is never rendered as itself`, () => {
      const masked = maskIdentifier(sample)
      expect(masked).not.toBeNull()
      expect(masked).not.toBe(sample)
      expect(masked).toContain('*')
    })
  }
})

describe('maskOpaque', () => {
  it('reveals one character at most', () => {
    expect(maskOpaque('acme')).toBe('a***')
    expect(maskOpaque('x')).toBe('***')
    expect(maskOpaque('')).toBe('***')
  })
})

describe('shortId', () => {
  it('shortens long ids and leaves short ones alone', () => {
    expect(shortId('3f2a1b7c-9d4e-4f10-9a2b-1c8d7e6f5a4b')).toBe('3f2a1b7c…')
    expect(shortId('abc')).toBe('abc')
  })
})

describe('subjectLabel', () => {
  it('prefers a masked identifier when the API sends one', () => {
    expect(subjectLabel('3f2a1b7c-9d4e', 'nam@gmail.com', 'email')).toBe('n***@gmail.com')
  })

  it('names the subject by id when no identifier is available', () => {
    // The admin queue endpoint returns only the subject id today, by design.
    expect(subjectLabel('3f2a1b7c-9d4e-4f10-9a2b-1c8d7e6f5a4b')).toBe('chủ thể 3f2a1b7c…')
    expect(subjectLabel('3f2a1b7c-9d4e-4f10-9a2b-1c8d7e6f5a4b', '')).toBe('chủ thể 3f2a1b7c…')
  })
})
