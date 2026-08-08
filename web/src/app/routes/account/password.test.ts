import { describe, expect, it } from 'vitest'
import { checkPassword, MIN_LENGTH, type PasswordRule, type RuleId } from './password'

function rule(pw: string, id: RuleId, ctx?: Parameters<typeof checkPassword>[1]): PasswordRule {
  const found = checkPassword(pw, ctx).rules.find((r) => r.id === id)
  if (!found) throw new Error(`rule ${id} was not returned`)
  return found
}

describe('length', () => {
  it('passes at exactly the minimum', () => {
    expect(rule('x'.repeat(MIN_LENGTH) + 'Qz9!', 'length').state).toBe('pass')
    expect(rule('Qz9!' + 'abcdefgh', 'length').state).toBe('pass')
  })

  it('fails one character short and says how many are missing', () => {
    const r = rule('Qz9!abcdefg', 'length')
    expect(r.state).toBe('fail')
    expect(r.detail).toContain('1')
  })

  it('counts characters, not bytes, so diacritics are not double credited', () => {
    // Twelve Vietnamese characters is twelve characters here and twenty-odd
    // bytes on the server. Erring towards the stricter of the two is deliberate.
    expect(rule('đườngxavắng!', 'length').state).toBe('pass')
    expect(rule('đườngxa', 'length').state).toBe('fail')
  })

  it('is unknown, not failing, while the field is still empty', () => {
    expect(rule('', 'length').state).toBe('unknown')
  })
})

describe('breach list', () => {
  it('never reports a pass, because the browser cannot check one', () => {
    const states = ['Sông Hồng chảy chậm 2026', 'Qz9!abcdefghij'].map(
      (pw) => rule(pw, 'breached').state,
    )
    expect(states).toEqual(['unknown', 'unknown'])
  })

  it('explains why it is unknown', () => {
    expect(rule('Sông Hồng chảy chậm 2026', 'breached').detail).toBeTruthy()
  })

  it('catches the obvious ones even when padded to a legal length', () => {
    for (const pw of ['password1234', 'matkhau12345', 'qwertyuiop12']) {
      expect(rule(pw, 'breached').state, pw).toBe('fail')
    }
  })

  it('sees through leetspeak', () => {
    expect(rule('P@ssw0rd!2026', 'breached').state).toBe('fail')
  })

  it('catches straight runs and single repeated characters', () => {
    for (const pw of ['123456789012', 'abcdefghijkl', 'aaaaaaaaaaaa']) {
      expect(rule(pw, 'breached').state, pw).toBe('fail')
    }
  })

  it('does not fire on an ordinary passphrase that merely contains a word', () => {
    expect(rule('con mèo ngủ trên nóc tủ', 'breached').state).toBe('unknown')
  })
})

describe('reuse', () => {
  it('is unknown when the old password is not available, as during a reset', () => {
    const r = rule('Qz9!abcdefghij', 'reuse')
    expect(r.state).toBe('unknown')
    expect(r.detail).toBeTruthy()
  })

  it('fails when the new password equals the current one', () => {
    expect(rule('Qz9!abcdefghij', 'reuse', { currentPassword: 'Qz9!abcdefghij' }).state).toBe('fail')
  })

  it('passes when they differ', () => {
    expect(rule('Qz9!abcdefghij', 'reuse', { currentPassword: 'something else 12' }).state).toBe(
      'pass',
    )
  })
})

describe('email in the password', () => {
  it('is not offered as a rule when there is no address to compare against', () => {
    expect(checkPassword('Qz9!abcdefghij').rules.some((r) => r.id === 'personal')).toBe(false)
  })

  it('fails when the address local part appears in the password', () => {
    expect(rule('an.nguyen-2026-ok', 'personal', { email: 'an.nguyen@acme.vn' }).state).toBe('fail')
  })

  it('passes otherwise', () => {
    expect(rule('Qz9!abcdefghij', 'personal', { email: 'an.nguyen@acme.vn' }).state).toBe('pass')
  })

  it('ignores an address too short to be meaningful', () => {
    expect(checkPassword('Qz9!abcdefghij', { email: 'a@acme.vn' }).rules.some((r) => r.id === 'personal')).toBe(
      false,
    )
  })
})

describe('overall verdict', () => {
  it('is not ok while empty', () => {
    expect(checkPassword('').ok).toBe(false)
  })

  it('is ok when every decidable rule passes, even with unknowns left', () => {
    const check = checkPassword('con mèo ngủ trên nóc tủ', { currentPassword: 'khác hẳn 12345' })
    expect(check.rules.some((r) => r.state === 'unknown')).toBe(true)
    expect(check.ok).toBe(true)
  })

  it('is not ok when any rule fails', () => {
    expect(checkPassword('password1234').ok).toBe(false)
  })
})

describe('strength score', () => {
  it('is zero for an empty field and for anything that failed a rule', () => {
    expect(checkPassword('').score).toBe(0)
    expect(checkPassword('password1234').score).toBe(0)
  })

  it('rewards length over punctuation', () => {
    const longPhrase = checkPassword('con mèo ngủ trên nóc tủ nhà bà')
    const shortScramble = checkPassword('Qz9!Kp2#Xm4')
    expect(longPhrase.score).toBeGreaterThan(shortScramble.score)
  })

  it('reaches the top of the scale for a long varied password', () => {
    expect(checkPassword('Sông Hồng chảy chậm 2026!').score).toBe(4)
  })

  it('always has a label matching the score', () => {
    for (const pw of ['', 'abc', 'Qz9!abcdefg', 'Qz9!abcdefghij', 'Sông Hồng chảy chậm 2026!']) {
      expect(checkPassword(pw).scoreLabel, pw).toBeTruthy()
    }
  })
})
