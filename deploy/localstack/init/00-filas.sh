#!/usr/bin/env bash
# Provisiona as filas FIFO e a politica de redrive.
#
# Este arquivo e executado uma vez, no start do LocalStack. As filas nascem aqui por
# codigo, e nao por clique no console, porque uma fila criada a mao e uma fila que o
# ambiente de outra pessoa nao tem.
#
# As decisoes de politica moram aqui e nao no consumidor, porque a fila e quem
# garante reentrega e descarte. Um consumidor que so apaga mensagem em caso de
# sucesso precisa de uma politica em algum lugar, e deixar essa politica escondida
# em um parametro de configuracao a torna invisivel para quem le a fila.
set -euo pipefail

FILA_OPERACOES="wager-transactions.fifo"
FILA_CARTAO_MORTO="wager-transactions-dlq.fifo"

# Long polling: sem ele o consumidor gasta uma requisicao HTTP por ciclo mesmo sem
# mensagem nenhuma. Com ele a chamada espera no servidor ate chegar algo ou esgotar
# o tempo, e o custo de ocioso cai de uma requisicao por segundo para uma chamada a
# cada vinte segundos.
LONG_POLLING=20

# VisibilityTimeout precisa ser maior que o pior processamento. O consumidor processa
# uma operacao por vez dentro de uma transacao curta; sessenta segundos deixa folga
# larga. Se fosse curto demais, uma instancia lenta perderia a mensagem para outra
# antes de confirmar, e a segunda processaria o que a primeira ja tinha aplicado. A
# transacao do banco e a garantia real contra isso; o timeout e a politica de
# redistribuicao.
VISIBILITY_TIMEOUT=60

# A cartao morto tem timeout maior: a mensagem que chega la ja falhou varias vezes,
# e dar mais tempo evita que uma leitura lenta no console a empurre para outra fila.
VISIBILITY_TIMEOUT_DLQ=120

# MaxReceiveCount e quantas vezes a mensagem volta antes de ir para o cartao morto.
# Tres e o equilibrio entre absorver uma falha transitoria de banco e nao ficar
# reentregando para sempre uma mensagem cujo defeito nao passa.
MAX_RECEIVE=3

TRABALHO=$(mktemp -d)
trap 'rm -rf "$TRABALHO"' EXIT

echo "[filas] criando $FILA_CARTAO_MORTO"

# Os atributos vao por arquivo e nao na forma abreviada de --attributes. A forma
# abreviada separa os pares por virgula, e um valor JSON de redrive tem virgulas
# dentro: o resultado e um erro de parse que fala de aspas e nao de fila.
cat > "$TRABALHO/atributos-cartao-morto.json" <<JSON
{
  "FifoQueue": "true",
  "ContentBasedDeduplication": "false",
  "ReceiveMessageWaitTimeSeconds": "0",
  "VisibilityTimeout": "$VISIBILITY_TIMEOUT_DLQ"
}
JSON

awslocal sqs create-queue \
  --queue-name "$FILA_CARTAO_MORTO" \
  --attributes "file://$TRABALHO/atributos-cartao-morto.json" \
  > /dev/null

URL_CARTAO_MORTO=$(awslocal sqs get-queue-url \
  --queue-name "$FILA_CARTAO_MORTO" --query QueueUrl --output text)
ARN_CARTAO_MORTO=$(awslocal sqs get-queue-attributes \
  --queue-url "$URL_CARTAO_MORTO" --attribute-names QueueArn \
  --query 'Attributes.QueueArn' --output text)

echo "[filas] cartao morto em $ARN_CARTAO_MORTO"

echo "[filas] criando $FILA_OPERACOES"

# A redrive permite que a fila de origem seja destino de redrive. Sem a criacao
# falha por permissao, e o erro nao menciona a politica, o que manda quem investiga
# procurar no lugar errado.
cat > "$TRABALHO/atributos-operacoes.json" <<JSON
{
  "FifoQueue": "true",
  "ContentBasedDeduplication": "false",
  "ReceiveMessageWaitTimeSeconds": "$LONG_POLLING",
  "VisibilityTimeout": "$VISIBILITY_TIMEOUT",
  "RedrivePolicy": "{\"maxReceiveCount\":\"$MAX_RECEIVE\",\"deadLetterTargetArn\":\"$ARN_CARTAO_MORTO\"}",
  "RedriveAllowPolicy": "{\"redrivePermission\":\"byQueue\",\"sourceQueueArns\":[\"*\"]}"
}
JSON

awslocal sqs create-queue \
  --queue-name "$FILA_OPERACOES" \
  --attributes "file://$TRABALHO/atributos-operacoes.json" \
  > /dev/null

URL_OPERACOES=$(awslocal sqs get-queue-url \
  --queue-name "$FILA_OPERACOES" --query QueueUrl --output text)

echo "[filas] operacoes em $URL_OPERACOES"
echo "[filas] prontas"