import { describe, expect, it } from 'vitest'
import { chatPath, describeLoaded, describeModel, summarise } from './HostOllama'
import type { OllamaModel } from '../types'

const llama: OllamaModel = {
  name: 'llama3.2:latest',
  id: 'a80c4f17acd5',
  size: '2.0 GB',
  sizeBytes: 2_000_000_000,
  modified: '2 weeks ago',
}

describe('a model described in one line', () => {
  it("says ollama's own id, size and age", () => {
    expect(describeModel(llama)).toBe('a80c4f17acd5 · 2.0 GB · 2 weeks ago')
  })

  it('leaves out what ollama did not say', () => {
    expect(describeModel({ ...llama, modified: '' })).toBe('a80c4f17acd5 · 2.0 GB')
  })
})

describe('a loaded model described in one line', () => {
  it('says where it runs, with what window, for how long', () => {
    expect(
      describeLoaded({ size: '4.0 GB', processor: '100% GPU', context: '4096', until: '4 minutes from now' }),
    ).toBe('4.0 GB · 100% GPU · 4096 context · until 4 minutes from now')
  })

  it('manages without a context, which older versions do not report', () => {
    expect(describeLoaded({ size: '4.0 GB', processor: '100% CPU', until: 'Forever' })).toBe(
      '4.0 GB · 100% CPU · until Forever',
    )
  })
})

describe('what the collection comes to', () => {
  it('counts the models and adds up the disk', () => {
    expect(summarise([llama, { ...llama, name: 'qwen', sizeBytes: 4_700_000_000 }])).toBe(
      '2 models, 6.2 GB',
    )
  })

  it('counts one model in the singular', () => {
    expect(summarise([llama])).toBe('1 model, 1.9 GB')
  })

  it('leaves the size out where ollama gave none to add', () => {
    expect(summarise([{ ...llama, sizeBytes: 0 }])).toBe('1 model')
  })
})

describe('the way to a conversation', () => {
  it('is the terminal with ollama run typed in, the name kept whole', () => {
    expect(chatPath(7, 'hf.co/org/model:Q4_K_M')).toBe(
      '/hosts/7/shell?run=' + encodeURIComponent('ollama run hf.co/org/model:Q4_K_M'),
    )
    expect(decodeURIComponent(chatPath(7, 'llama3.2:3b').split('=')[1])).toBe('ollama run llama3.2:3b')
  })
})
