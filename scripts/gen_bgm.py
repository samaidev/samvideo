#!/usr/bin/env python3
"""生成带旋律的背景音乐 - 3种风格"""
import numpy as np
import struct, wave, os, sys

SR = 44100  # 采样率

def note_freq(name):
    """音符名转频率"""
    notes = {'C':0,'C#':1,'D':2,'D#':3,'E':4,'F':5,'F#':6,'G':7,'G#':8,'A':9,'A#':10,'B':11}
    if len(name) == 2:
        n, o = name[0], int(name[1])
    else:
        n, o = name[:2], int(name[2])
    midi = 12 * (o + 1) + notes[n]
    return 440.0 * 2 ** ((midi - 69) / 12)

def adsr(length, sr=SR, attack=0.01, decay=0.1, sustain=0.6, release=0.3):
    """ADSR 包络 - 模拟钢琴音色"""
    n = int(length * sr)
    env = np.zeros(n)
    a = int(attack * sr)
    d = int(decay * sr)
    s = int(sustain * sr)
    r = int(release * sr)
    if a + d + s + r > n:
        # 缩放到实际长度
        a = int(n * 0.05)
        d = int(n * 0.15)
        r = int(n * 0.3)
        s = n - a - d - r
        if s < 0:
            s = 0
            a = n // 5
            d = n // 5
            r = n - a - d
    if a > 0:
        env[:a] = np.linspace(0, 1, a)
    if d > 0:
        env[a:a+d] = np.linspace(1, sustain, d)
    if s > 0:
        env[a+d:a+d+s] = sustain
    if r > 0:
        env[a+d+s:a+d+s+r] = np.linspace(sustain, 0, r)
    if len(env) < n:
        env = np.pad(env, (0, n - len(env)))
    return env[:n]

def piano_note(freq, duration, sr=SR):
    """合成单个钢琴音符 - 基频 + 泛音 + ADSR"""
    n = int(duration * sr)
    t = np.linspace(0, duration, n, endpoint=False)
    # 钢琴音色：基频 + 2倍频 + 3倍频 + 4倍频，振幅递减
    wave = (
        0.5 * np.sin(2 * np.pi * freq * t) +
        0.25 * np.sin(2 * np.pi * 2 * freq * t) +
        0.12 * np.sin(2 * np.pi * 3 * freq * t) +
        0.06 * np.sin(2 * np.pi * 4 * freq * t)
    )
    env = adsr(duration, sr)
    if len(env) < len(wave):
        env = np.pad(env, (0, len(wave) - len(env)))
    return wave * env

def render_melody(notes, sr=SR, gap=0.05):
    """渲染旋律 - notes: [(freq, duration), ...]"""
    parts = []
    for freq, dur in notes:
        if freq > 0:
            parts.append(piano_note(freq, dur, sr))
        else:
            parts.append(np.zeros(int(dur * sr)))
        # 音符间间隔
        parts.append(np.zeros(int(gap * sr)))
    return np.concatenate(parts) if parts else np.zeros(sr)

def add_reverb(audio, sr=SR, delay_ms=150, decay=0.3, repeats=4):
    """简单回声/混响"""
    result = audio.copy()
    for i in range(1, repeats + 1):
        delay = int(sr * delay_ms / 1000 * i)
        echoed = np.zeros_like(audio)
        if delay < len(audio):
            echoed[delay:] = audio[:-delay] * (decay ** i)
        result += echoed
    return result

def normalize(audio, target_db=-12):
    """归一化到目标音量"""
    peak = np.max(np.abs(audio))
    if peak == 0:
        return audio
    target = 10 ** (target_db / 20)
    gain = target / peak
    return audio * gain

def save_wav(audio, path, sr=SR):
    """保存为 WAV"""
    audio = np.clip(audio, -1.0, 1.0)
    audio_int16 = (audio * 32767).astype(np.int16)
    with wave.open(path, 'w') as w:
        w.setnchannels(1)
        w.setsampwidth(2)
        w.setframerate(sr)
        w.writeframes(audio_int16.tobytes())
    print(f"saved: {path} ({len(audio)/sr:.1f}s, {os.path.getsize(path)/1024:.0f}KB)")

def gen_ambient_piano():
    """风格1: 轻柔钢琴 - C大调慢板旋律"""
    # C大调旋律：C-E-G-C-E-G-A-G-E-C...
    melody_notes = [
        ('C4', 1.0), ('E4', 1.0), ('G4', 1.0), ('C5', 1.5),
        ('G4', 0.5), ('E4', 1.0), ('A4', 1.0), ('G4', 1.5),
        ('E4', 0.5), ('C4', 1.0), ('D4', 1.0), ('E4', 1.0),
        ('F4', 1.0), ('E4', 1.0), ('D4', 1.0), ('C4', 2.0),
        ('G4', 1.0), ('E4', 1.0), ('C4', 1.0), ('G4', 1.5),
        ('A4', 0.5), ('G4', 1.0), ('E4', 1.0), ('C4', 2.0),
    ]
    notes = [(note_freq(n) if n != 'rest' else 0, d) for n, d in melody_notes]
    audio = render_melody(notes, gap=0.08)
    # 加低音和弦伴奏
    bass_notes = [
        ('C3', 4.0), ('G3', 4.0), ('A3', 4.0), ('F3', 4.0),
        ('C3', 4.0), ('G3', 4.0), ('C3', 4.0), ('G3', 4.0),
    ]
    bass = render_melody([(note_freq(n), d) for n, d in bass_notes], gap=0.02)
    # 对齐长度
    if len(bass) < len(audio):
        bass = np.pad(bass, (0, len(audio) - len(bass)))
    else:
        bass = bass[:len(audio)]
    audio = audio * 0.7 + bass * 0.3
    audio = add_reverb(audio)
    audio = normalize(audio, target_db=-6)
    # 循环到 60 秒
    while len(audio) < SR * 60:
        audio = np.concatenate([audio, audio])
    return audio[:SR * 60]

def gen_corporate_uplifting():
    """风格2: 企业宣传 - D大调明亮旋律，有节奏感"""
    melody_notes = [
        ('D4', 0.5), ('F4', 0.5), ('A4', 0.5), ('D5', 0.5),
        ('A4', 0.5), ('F4', 0.5), ('A4', 1.0),
        ('G4', 0.5), ('B4', 0.5), ('D5', 0.5), ('G5', 0.5),
        ('D5', 0.5), ('B4', 0.5), ('D5', 1.0),
        ('A4', 0.5), ('D5', 0.5), ('F5', 0.5), ('A5', 0.5),
        ('F5', 0.5), ('D5', 0.5), ('A4', 1.0),
        ('D5', 0.5), ('A4', 0.5), ('F4', 0.5), ('D4', 0.5),
        ('F4', 0.5), ('A4', 0.5), ('D4', 1.5),
    ]
    notes = [(note_freq(n), d) for n, d in melody_notes]
    audio = render_melody(notes, gap=0.04)
    # 加和弦伴奏
    chord_notes = [
        ('D3', 2.0), ('A3', 2.0), ('G3', 2.0), ('D4', 2.0),
        ('A3', 2.0), ('D3', 2.0), ('G3', 2.0), ('A3', 2.0),
    ]
    chord = render_melody([(note_freq(n), d) for n, d in chord_notes], gap=0.02)
    if len(chord) < len(audio):
        chord = np.pad(chord, (0, len(audio) - len(chord)))
    else:
        chord = chord[:len(audio)]
    audio = audio * 0.6 + chord * 0.4
    audio = add_reverb(audio, delay_ms=120, decay=0.25, repeats=3)
    audio = normalize(audio, target_db=-6)
    while len(audio) < SR * 60:
        audio = np.concatenate([audio, audio])
    return audio[:SR * 60]

def gen_tech_electronic():
    """风格3: 科技电子 - E小调，低音脉冲 + 高音点缀"""
    # 低音脉冲
    bass_pattern = [
        ('E2', 0.25), (0, 0.05), ('E2', 0.25), (0, 0.05),
        ('E2', 0.25), (0, 0.05), ('E2', 0.25), (0, 0.05),
        ('G2', 0.25), (0, 0.05), ('G2', 0.25), (0, 0.05),
        ('B2', 0.25), (0, 0.05), ('B2', 0.25), (0, 0.05),
    ] * 4
    bass = render_melody([(note_freq(n) if isinstance(n, str) else 0, d) for n, d in bass_pattern], gap=0)
    # 高音旋律
    melody_notes = [
        ('E4', 0.5), ('G4', 0.5), ('B4', 0.5), ('E5', 1.0),
        ('B4', 0.5), ('G4', 0.5), ('E4', 1.0),
        ('D5', 0.5), ('B4', 0.5), ('G4', 0.5), ('D5', 1.0),
        ('B4', 0.5), ('G4', 0.5), ('E4', 1.5),
    ] * 2
    melody = render_melody([(note_freq(n), d) for n, d in melody_notes], gap=0.05)
    # 对齐
    max_len = max(len(bass), len(melody))
    bass = np.pad(bass, (0, max_len - len(bass)))
    melody = np.pad(melody, (0, max_len - len(melody)))
    audio = bass * 0.5 + melody * 0.5
    audio = add_reverb(audio, delay_ms=80, decay=0.2, repeats=3)
    audio = normalize(audio, target_db=-6)
    while len(audio) < SR * 60:
        audio = np.concatenate([audio, audio])
    return audio[:SR * 60]

if __name__ == '__main__':
    out_dir = sys.argv[1] if len(sys.argv) > 1 else '.'
    os.makedirs(out_dir, exist_ok=True)

    print("generating ambient_piano...")
    audio = gen_ambient_piano()
    save_wav(audio, os.path.join(out_dir, 'ambient_piano.wav'))

    print("generating corporate_uplifting...")
    audio = gen_corporate_uplifting()
    save_wav(audio, os.path.join(out_dir, 'corporate_uplifting.wav'))

    print("generating tech_electronic...")
    audio = gen_tech_electronic()
    save_wav(audio, os.path.join(out_dir, 'tech_electronic.wav'))

    print("done!")
