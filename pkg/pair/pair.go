package pair

import (
	"bytes"
	"crypto/ecdh"
	"crypto/rand"
	"errors"
	"fmt"

	"github.com/avereha/pod/pkg/message"

	"github.com/davecgh/go-spew/spew"
	"github.com/jacobsa/crypto/cmac"
	log "github.com/sirupsen/logrus"
)

const (
	sp1 = "SP1="
	sp2 = ",SP2="

	sps0   = "SPS0="
	sps1   = "SPS1="
	sps2   = "SPS2="
	sp0gp0 = "SP0,GP0"
	p0     = "P0="
)

type Pair struct {
	podPublic  []byte
	podPrivate []byte
	podNonce   []byte
	podConf    []byte

	pdmPublic []byte
	pdmNonce  []byte
	pdmConf   []byte
	sps0      []byte

	sharedSecret []byte
	pdmID        []byte
	podID        []byte

	ltk     []byte
	confKey []byte // key used to sign the "Conf" values
}

func parseStringByte(expectedNames []string, data []byte) (map[string][]byte, error) {
	ret := make(map[string][]byte)
	for _, name := range expectedNames {
		n := len(name)
		if string(data[:n]) != name {
			return nil, fmt.Errorf("Name not found %s in %x", name, data)
		}
		data = data[n:]
		length := int(data[0])<<8 | int(data[1])
		ret[name] = data[2 : 2+length]
		log.Tracef("Read field: %s :: %x :: %d", name, ret[name], len(ret[name]))

		data = data[2+length:]
	}
	return ret, nil
}

func buildStringByte(names []string, values map[string][]byte) ([]byte, error) {
	var buf bytes.Buffer
	for _, name := range names {
		buf.WriteString(name)
		n := len(values[name])
		buf.WriteByte(byte(n >> 8 & 0xff))
		buf.WriteByte(byte(n & 0xff))
		buf.Write(values[name])
	}
	return buf.Bytes(), nil
}

func (c *Pair) ParseSP1SP2(msg *message.Message) error {
	log.Infof("Received SP1 SP2 payload %x", msg.Payload)

	sp, err := parseStringByte([]string{sp1, sp2}, msg.Payload)
	if err != nil {
		log.Debugf("Message :%s", spew.Sdump(msg))
		return err
	}

	log.Infof("Received SP1 SP2: %x :: %x", sp[sp1], sp[sp2])
	c.podID = msg.Destination
	c.pdmID = msg.Source
	return nil
}

func (c *Pair) ParseSPS0(msg *message.Message) error {
	sp, err := parseStringByte([]string{sps0}, msg.Payload)
	if err != nil {
		log.Debugf("Message :%s", spew.Sdump(msg))
		return err
	}

	log.Infof("Received SPS0  %x", sp[sps0])
	copy(c.pdmNonce, []byte(sps0))

	err = c.computeMyData()
	if err != nil {
		return err
	}
	return nil
}

func (c *Pair) ParseSPS1(msg *message.Message) error {
	sp, err := parseStringByte([]string{sps1}, msg.Payload)
	if err != nil {
		log.Debugf("Message :%s", spew.Sdump(msg))
		return err
	}
	log.Infof("Received SPS1  %x", sp[sps1])
	pdmPublic := sp[sps1][:64]
	c.pdmPublic = make([]byte, 64)
	copy(c.pdmPublic, pdmPublic)

	pdmNonce := sp[sps1][64:]
	c.pdmNonce = make([]byte, 16)
	copy(c.pdmNonce, pdmNonce)
	log.Debugf("Pdm Public  %x :: %d", c.pdmPublic, len(c.pdmPublic))
	log.Debugf("Pdm Nonce   %x :: %d", c.pdmNonce, len(c.pdmNonce))

	// c.curve25519LTK, err = curve25519.X25519(c.podPrivate, c.pdmPublic)
	if err != nil {
		return err
	}
	return nil
}

func (c *Pair) GenerateSPS0() (*message.Message, error) {
	var err error
	var buf bytes.Buffer
	//000109a218
	//0000099129
	buf.WriteByte(0x00)
	buf.WriteByte(0x00)
	buf.WriteByte(0x09)
	buf.WriteByte(0x91)
	buf.WriteByte(0x29)

	sp := make(map[string][]byte)
	sp[sps0] = buf.Bytes()

	msg := message.NewMessage(message.MessageTypePairing, c.podID, c.pdmID)
	msg.Payload, err = buildStringByte([]string{sps0}, sp)
	if err != nil {
		return nil, err
	}
	log.Debugf("Sending SPS0: %x", msg.Payload)
	return msg, nil
}

func (c *Pair) GenerateSPS1() (*message.Message, error) {
	var err error
	var buf bytes.Buffer

	buf.Write(c.podPublic)
	buf.Write(c.podNonce)

	sp := make(map[string][]byte)
	sp[sps1] = buf.Bytes()

	msg := message.NewMessage(message.MessageTypePairing, c.podID, c.pdmID)
	msg.Payload, err = buildStringByte([]string{sps1}, sp)
	if err != nil {
		return nil, err
	}
	err = c.computePairData()
	if err != nil {
		return nil, err
	}
	log.Infof("Sending SPS1: %x", msg.Payload)
	return msg, nil
}

func (c *Pair) ParseSPS2(msg *message.Message) error {
	sp, err := parseStringByte([]string{sps2}, msg.Payload)
	if err != nil {
		log.Infof("SPS2 Message :%s", spew.Sdump(msg))
		return err
	}

	if !bytes.Equal(c.pdmConf, sp[sps2]) {
		return fmt.Errorf("Invalid conf value. Expected: %x. Got %x", c.pdmConf, sp[sps2])
	}
	log.Infof("Validated PDM SPS2: %x", sp[sps2])
	return nil
}

func (c *Pair) GenerateSPS2() (*message.Message, error) {
	var err error
	sp := make(map[string][]byte)
	sp[sps2] = c.podConf

	msg := message.NewMessage(message.MessageTypePairing, c.podID, c.pdmID)
	msg.Payload, err = buildStringByte([]string{sps2}, sp)
	if err != nil {
		return nil, err
	}
	log.Debugf("Generated SPS2: %x", msg.Payload)
	return msg, nil
}

func (c *Pair) ParseSP0GP0(msg *message.Message) error {
	if string(msg.Payload) != sp0gp0 {
		log.Debugf("Message :%s", spew.Sdump(msg))
		return fmt.Errorf("Expected SP0GP0, got %x", msg.Payload)
	}
	log.Debugf("Parsed SP0GP0")
	return nil
}

func (c *Pair) GenerateP0() (*message.Message, error) {
	var err error
	msg := message.NewMessage(message.MessageTypePairing, c.podID, c.pdmID)
	sp := make(map[string][]byte)
	sp[p0] = []byte{0xa5} // magic constant ???
	msg.Payload, err = buildStringByte([]string{p0}, sp)
	log.Debugf("Generated P0")

	return msg, err
}

func (c *Pair) LTK() ([]byte, error) {
	if c.sharedSecret != nil {
		return c.ltk, nil
	}
	return nil, errors.New("Missing  enough data to compute LTK")
}

func (c *Pair) computeMyData() error {
	var err error
	c.podPrivate = make([]byte, 32)
	c.podPublic = make([]byte, 64)
	c.podNonce = make([]byte, 16)

	rand.Read(c.podNonce)
	podPrivate, _ := ecdh.P256().GenerateKey(rand.Reader)
	c.podPrivate = podPrivate.Bytes()
	c.podPublic = podPrivate.PublicKey().Bytes()[1:]
	log.Infof("Pod Private %x :: %d", c.podPrivate, len(c.podPrivate))
	log.Infof("Pod Public  %x :: %d", c.podPublic, len(c.podPublic))
	log.Infof("Pod Nonce   %x :: %d", c.podNonce, len(c.podNonce))
	return err

}
func (c *Pair) computePairData() error {
	var err error
	// fill in: lrtk, podConf, pdmConf, intermediaryKey
	privateKey, err := ecdh.P256().NewPrivateKey(c.podPrivate)
	if err != nil {
		return err
	}
	publicKey, err := ecdh.P256().NewPublicKey(append([]byte{0x04}, c.pdmPublic...))
	if err != nil {
		return err
	}
	c.sharedSecret, err = privateKey.ECDH(publicKey)
	if err != nil {
		return err
	}
	log.Infof("Donna LTK %x :: %d", c.sharedSecret, len(c.sharedSecret))

	//first_key = data.pod_public[-4:] + data.pdm_public[-4:] + data.pod_nonce[-4:] + data.pdm_nonce[-4:]
	var endSize = 4
	firstKey := append(c.podPublic[len(c.podPublic)-endSize:], c.pdmPublic[len(c.pdmPublic)-endSize:]...)
	firstKey = append(firstKey, c.podNonce[len(c.podNonce)-endSize:]...)
	firstKey = append(firstKey, c.pdmNonce[len(c.pdmNonce)-endSize:]...)
	log.Infof("First key %x :: %d", firstKey, len(firstKey))

	first, err := cmac.New(firstKey)
	if err != nil {
		return err
	}
	log.Infof("CMAC: %d", first.Size())
	first.Write(c.sharedSecret)
	intermediarKey := first.Sum([]byte{})

	log.Infof("Intermediary key %x :: %d", intermediarKey, len(intermediarKey))

	// bb_data = bytes.fromhex("01") + bytes("TWIt", "ascii") + data.pod_nonce + data.pdm_nonce + bytes.fromhex("0001")
	var bbData bytes.Buffer
	bbData.WriteByte(0x01)
	bbData.WriteString("TWIt")
	bbData.Write(c.podNonce)
	bbData.Write(c.pdmNonce)
	bbData.WriteByte(0x00)
	bbData.WriteByte(0x01)
	bbHash, err := cmac.New(intermediarKey)
	if err != nil {
		return err
	}
	bbHash.Write(bbData.Bytes())
	c.confKey = bbHash.Sum([]byte{})
	log.Infof("Conf key %x :: %d", c.ltk, len(c.ltk))

	// ab_data = bytes.fromhex("02") + bytes("TWIt", "ascii") + data.pod_nonce + data.pdm_nonce + bytes.fromhex("0001")
	var abData bytes.Buffer
	abData.WriteByte(0x02) // this is the only difference
	abData.WriteString("TWIt")
	abData.Write(c.podNonce)
	abData.Write(c.pdmNonce)
	abData.WriteByte(0x00)
	abData.WriteByte(0x01)
	abHash, err := cmac.New(intermediarKey)
	if err != nil {
		return err
	}
	abHash.Write(abData.Bytes())
	c.ltk = abHash.Sum([]byte{})
	log.Infof("Long Term key %x :: %d", c.ltk, len(c.ltk))

	//  pdm_conf_data = bytes("KC_2_U", "ascii") + data.pdm_nonce + data.pod_nonce
	var pdmConfData bytes.Buffer
	pdmConfData.WriteString("KC_2_U")
	pdmConfData.Write(c.pdmNonce)
	pdmConfData.Write(c.podNonce)
	hash, err := cmac.New(c.confKey)
	if err != nil {
		return err
	}
	hash.Write(pdmConfData.Bytes())
	c.pdmConf = hash.Sum([]byte{})
	log.Infof("PDM Conf %x :: %d", c.pdmConf, len(c.pdmConf))

	//  pdm_conf_data = bytes("KC_2_V", "ascii") + data.pdm_nonce + data.pod_nonce
	var podConfData bytes.Buffer
	podConfData.WriteString("KC_2_V")
	podConfData.Write(c.podNonce) // ???
	podConfData.Write(c.pdmNonce)
	hash, err = cmac.New(c.confKey)
	if err != nil {
		return err
	}
	hash.Write(podConfData.Bytes())
	c.podConf = hash.Sum([]byte{})
	log.Infof("Pod Conf %x :: %d", c.podConf, len(c.podConf))

	return nil
}
