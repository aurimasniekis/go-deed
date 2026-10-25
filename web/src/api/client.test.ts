import { describe, expect, it } from 'vitest'
import { ApiError, check, errorMessage, unwrap } from './client'

const res = (status: number) => new Response(null, { status })

describe('API client helpers', () => {
  it('unwraps data and turns problems into ApiError with code, action and fields', () => {
    expect(unwrap({ data: { a: 1 }, response: res(200) })).toEqual({ a: 1 })
    try {
      unwrap({
        error: { detail: 'invalid', code: 'validation_failed', fields: [{ field: 'name', message: 'required' }] },
        response: res(422),
      })
      throw new Error('expected a throw')
    } catch (e) {
      expect(e).toBeInstanceOf(ApiError)
      const err = e as ApiError
      expect(err.status).toBe(422)
      expect(err.code).toBe('validation_failed')
      expect(err.fields).toHaveLength(1)
      expect(err.message).toBe('invalid — name: required')
    }
  })

  it('reports reauth actions and status helpers', () => {
    const err = new ApiError({ code: 'reauth_required', action: 'reauth', detail: 'recent sign-in needed' }, 403)
    expect(err.forbidden).toBe(true)
    expect(err.notFound).toBe(false)
    expect(err.action).toBe('reauth')
  })

  it('check() throws for failed 204 endpoints', () => {
    expect(() => check({ response: res(204) })).not.toThrow()
    expect(() => check({ error: { detail: 'nope' }, response: res(404) })).toThrow('nope')
  })

  it('formats any error', () => {
    expect(errorMessage(new Error('x'))).toBe('x')
    expect(errorMessage('y')).toBe('y')
    expect(errorMessage(42)).toBe('Something went wrong')
  })
})
